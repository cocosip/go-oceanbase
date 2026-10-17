package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func batchArrayCandidate(request batchRequest) bool {
	if request.options.Strategy != BatchArrayBinding || !request.options.ContinueOnError || len(request.rows) < 2 || !batchScalarRows(request.rows) {
		return false
	}
	for _, row := range request.rows {
		for _, value := range row {
			if named, ok := value.(sql.NamedArg); ok {
				value = named.Value
			}
			// Anonymous array element metadata has no unsigned flag byte.
			switch value.(type) {
			case uint, uint64:
				return false
			}
		}
	}
	return true
}

func (c *conn) tryBatchArray(ctx context.Context, request batchRequest) (handled bool, err error) {
	if !batchArrayCandidate(request) || c.cfg.PrepareMode != PrepareServer {
		return false, nil
	}
	if err = c.lock(ctx); err != nil {
		return true, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return true, driver.ErrBadConn
	}
	if c.session.mode != ModeOracle || !supportsVersion(c.session.obVersion, 4, 2, 1) || c.activeTx == nil || c.activeTx.readOnly || c.session.ok.Status&2 != 0 || !batchDML(request.query, c.session.mode, c.session.ok.Status&512 != 0) || !c.batchParametersValid(request) {
		return false, nil
	}
	plan, bindErr := buildBindPlan(request.query, c.session.mode)
	if bindErr != nil || len(plan.slots) == 0 {
		return false, nil
	}
	c.clearDescriptors()
	if err = c.clearStatementsLocked(ctx); err != nil {
		request.result.Items[0].Err = err
		return true, batchFailure(request.result)
	}
	prepared, err := c.prepareServerLocked(ctx, request.query)
	if err != nil {
		request.result.Items[0].Err = err
		return true, batchFailure(request.result)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		if cleanupErr := errors.Join(prepared.closeLocked(cleanup), c.cleanupResourcesLocked(cleanup, false)); cleanupErr != nil {
			err = errors.Join(err, &BatchError{Index: -1, Completed: batchSucceeded(request.result), Err: cleanupErr})
		}
	}()
	request.result.Strategy = BatchArrayBinding
	return true, c.execBatchArrayLocked(ctx, request, prepared)
}

func (c *conn) encodeBatchArrayRow(ctx context.Context, prepared *stmt, row []any) ([]wire.Parameter, error) {
	values, err := executionArguments(row)
	if err != nil {
		return nil, err
	}
	return c.cursorParametersLocked(ctx, prepared.plan, values)
}

func (c *conn) execBatchArrayLocked(ctx context.Context, request batchRequest, prepared *stmt) error {
	query, err := c.encodeSQL(prepared.plan.sql)
	if err != nil {
		request.result.Items[0].Err = err
		return batchFailure(request.result)
	}
	maxRows := min(request.options.ChunkRows, c.cfg.Limits.MaxResultItems, request.options.ChunkParameters/len(prepared.plan.slots), 65535)
	for next := 0; next < len(request.rows); {
		if err = ctx.Err(); err != nil {
			request.result.Items[next].Err = err
			break
		}
		start := next
		var rows [][]wire.Parameter
		var dataBytes int64
		for next < len(request.rows) && len(rows) < maxRows {
			parameters, rowErr := c.encodeBatchArrayRow(ctx, prepared, request.rows[next])
			var added int64
			if rowErr == nil {
				for _, parameter := range parameters {
					added += parameter.EncodedSize()
				}
				size, sizeErr := wire.StmtArraySize(len(rows)+1, len(parameters), dataBytes+added)
				rowErr = sizeErr
				if rowErr == nil && size > c.cfg.Limits.MaxMessageBytes {
					rowErr = wire.ErrMessageTooLarge
				}
			}
			if len(rows) > 0 && (rowErr != nil || !sameParameterTypes(rows[0], parameters)) {
				break
			}
			if rowErr != nil {
				request.result.Items[next] = BatchItem{Status: BatchFailed, Err: rowErr}
				next++
				break
			}
			rows = append(rows, parameters)
			dataBytes += added
			next++
		}
		if len(rows) == 0 {
			continue
		}
		payload, encodeErr := wire.EncodeStmtExecuteArray(prepared.id, rows, c.cfg.Limits.MaxMessageBytes, c.executeOptions(query, false))
		if encodeErr != nil {
			request.result.Items[start].Err = encodeErr
			break
		}
		if err = c.runBatchArrayLocked(ctx, payload, request.result.Items[start:next]); err != nil {
			break
		}
	}
	return batchFailure(request.result)
}

func (c *conn) runBatchArrayLocked(ctx context.Context, payload []byte, items []BatchItem) (err error) {
	r := c.startRows(ctx, true, nil)
	r.keepGate = true
	r.cancelExecution = c.beginCancelExecution()
	ctx = r.ctx
	defer func() {
		r.err = err
		r.release()
		if r.err != nil {
			items[len(items)-1].Err = errors.Join(items[len(items)-1].Err, r.err)
		}
		err = r.err
	}()
	c.session.packets.ResetSequence()
	c.lastWarnings, c.warningsCleared = WarningSummary{}, false
	if err = c.session.packets.WriteMessage(ctx, payload); err != nil {
		if errorsPreSend(err) {
			items[0].Err = err
			return err
		}
		return c.batchRewriteUnknown(items, err)
	}
	for i := range items {
		items[i].Started = true
	}
	var budget int64
	data, err := c.readBatchArrayPacket(ctx, &budget)
	if err != nil {
		return c.batchRewriteUnknown(items, err)
	}
	var failures map[int]error
	if len(data) > 0 && data[0] != 0 {
		failures, err = c.readBatchArrayFailures(ctx, data, len(items), &budget)
		if err == nil {
			data, err = c.readBatchArrayPacket(ctx, &budget)
		}
	}
	if err != nil {
		return c.batchRewriteUnknown(items, err)
	}
	ok, err := wire.DecodeOK(data, c.session.capabilities)
	if err != nil || ok.Status&(8|64|128|4096|2) != 0 {
		if err == nil {
			err = wire.ErrMalformed
		}
		return c.batchRewriteUnknown(items, err)
	}
	c.observeResourceStatus(ok.Status)
	c.session.ok = ok
	c.lastWarnings = WarningSummary{Count: ok.Warnings, Known: true}
	for i := range items {
		items[i].Status = BatchSucceeded
		if failure := failures[i]; failure != nil {
			items[i].Status, items[i].Err = BatchFailed, failure
		} else if len(items) == 1 && ok.AffectedRows <= math.MaxInt64 {
			items[i].RowsAffected, items[i].RowsAffectedKnown = int64(ok.AffectedRows), true
		}
	}
	return nil
}

func (c *conn) readBatchArrayPacket(ctx context.Context, budget *int64) ([]byte, error) {
	data, err := c.session.packets.ReadMessage(ctx)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > c.cfg.Limits.MaxMessageBytes-*budget {
		return nil, wire.ErrMessageTooLarge
	}
	*budget += int64(len(data))
	if len(data) > 0 && data[0] == 255 {
		return nil, decodeServerError(data)
	}
	return data, nil
}

func (c *conn) readBatchArrayFailures(ctx context.Context, data []byte, count int, budget *int64) (map[int]error, error) {
	columns, err := wire.DecodeColumnCount(data, 3)
	if err != nil || columns != 3 {
		return nil, wire.ErrMalformed
	}
	metadata := make([]wire.Column, 3)
	for i := range metadata {
		data, err = c.readBatchArrayPacket(ctx, budget)
		if err != nil {
			return nil, err
		}
		metadata[i], err = c.decodeColumn(data)
		if err != nil {
			return nil, err
		}
	}
	// This native response always sends a legacy EOF, including with deprecated EOF negotiated.
	data, err = c.readBatchArrayPacket(ctx, budget)
	if err != nil {
		return nil, err
	}
	eof, err := wire.DecodeEOF(data)
	if err != nil || eof.Status&8 == 0 || metadata[0].InOut|metadata[1].InOut|metadata[2].InOut != 0 || metadata[0].Name != "sql_no" || metadata[0].Type != 8 || metadata[1].Name != "error_code" || metadata[1].Type != 8 || metadata[2].Name != "error_message" || metadata[2].Type != 253 {
		return nil, wire.ErrMalformed
	}
	failures := make(map[int]error)
	for {
		data, err = c.readBatchArrayPacket(ctx, budget)
		if err != nil {
			return nil, err
		}
		if len(data) > 0 && data[0] == 254 {
			eof, decodeErr := wire.DecodeEOF(data)
			if decodeErr != nil || eof.Status&8 == 0 || len(failures) == 0 {
				return nil, wire.ErrMalformed
			}
			return failures, nil
		}
		position, failure, decodeErr := c.decodeBatchArrayFailure(data, metadata, count)
		if decodeErr != nil || len(failures) >= count || failures[position] != nil {
			return nil, wire.ErrMalformed
		}
		failures[position] = failure
	}
}

func (c *conn) decodeBatchArrayFailure(data []byte, metadata []wire.Column, count int) (int, *Error, error) {
	fields, err := wire.DecodeBinaryRow(data, metadata)
	if err != nil || len(fields[0]) != 8 || len(fields[1]) != 8 || fields[2] == nil {
		return 0, nil, wire.ErrMalformed
	}
	position, code := binary.LittleEndian.Uint64(fields[0]), binary.LittleEndian.Uint64(fields[1])
	if position >= uint64(count) || code == 0 || code > math.MaxUint16 {
		return 0, nil, wire.ErrMalformed
	}
	message, err := c.decodeRowValue(metadata[2], true, fields[2])
	text, valid := message.(string)
	if err != nil || !valid {
		return 0, nil, wire.ErrMalformed
	}
	return int(position), &Error{Code: uint32(code), Message: text}, nil
}
