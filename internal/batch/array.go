package batch

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"math"
)

func (n *native) execBatchArrayLocked(ctx context.Context, request Request, prepared *statement.Statement) error {
	query, err := n.encodeSQL(prepared.Plan().SQL)
	if err != nil {
		request.Result.Items[0].Err = err
		return Failure(request.Result)
	}
	maxRows := min(request.Options.ChunkRows, n.op.Config().Limits.MaxResultItems, request.Options.ChunkParameters/len(prepared.Plan().Slots), 65535)
	for next := 0; next < len(request.Rows); {
		if err = ctx.Err(); err != nil {
			request.Result.Items[next].Err = err
			break
		}
		start := next
		var rows [][]wire.Parameter
		var dataBytes int64
		for next < len(request.Rows) && len(rows) < maxRows {
			parameters, rowErr := n.encodeParameters(ctx, prepared, request.Rows[next])
			var added int64
			if rowErr == nil {
				for _, parameter := range parameters {
					added += parameter.EncodedSize()
				}
				size, sizeErr := wire.StmtArraySize(len(rows)+1, len(parameters), dataBytes+added)
				rowErr = sizeErr
				if rowErr == nil && size > n.op.Config().Limits.MaxMessageBytes {
					rowErr = wire.ErrMessageTooLarge
				}
			}
			if len(rows) > 0 && (rowErr != nil || !sameParameterTypes(rows[0], parameters)) {
				break
			}
			if rowErr != nil {
				request.Result.Items[next] = Item{Status: Failed, Err: rowErr}
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
		payload, encodeErr := wire.EncodeStmtExecuteArray(prepared.ID(), rows, n.op.Config().Limits.MaxMessageBytes, statement.ExecuteOptions(n.op, query, false))
		if encodeErr != nil {
			request.Result.Items[start].Err = encodeErr
			break
		}
		if err = n.runBatchArrayLocked(ctx, payload, request.Result.Items[start:next]); err != nil {
			break
		}
	}
	return Failure(request.Result)
}

func (n *native) runBatchArrayLocked(ctx context.Context, payload []byte, items []Item) (err error) {
	boundary, beginErr := n.beginResponse(ctx, true)
	if beginErr != nil {
		items[0].Err = beginErr
		return beginErr
	}
	defer func() {
		err = boundary.finish(err)
		if err != nil {
			items[len(items)-1].Err = errors.Join(items[len(items)-1].Err, err)
		}
	}()
	n.op.Packets().ResetSequence()
	n.clearWarnings()
	if err = n.op.Packets().WriteMessage(ctx, payload); err != nil {
		if command.PreSend(err) {
			items[0].Err = err
			return err
		}
		n.outcome.Started = true
		return n.batchRewriteUnknown(items, err)
	}
	n.outcome.Started = true
	for i := range items {
		items[i].Started = true
	}
	var budget int64
	data, err := n.readBatchArrayPacket(ctx, &budget)
	if err != nil {
		return n.batchRewriteUnknown(items, err)
	}
	var failures map[int]error
	if len(data) > 0 && data[0] != 0 {
		failures, err = n.readBatchArrayFailures(ctx, data, len(items), &budget)
		if err == nil {
			data, err = n.readBatchArrayPacket(ctx, &budget)
		}
	}
	if err != nil {
		return n.batchRewriteUnknown(items, err)
	}
	ok, err := wire.DecodeOK(data, n.op.Protocol().Capabilities)
	if err != nil || ok.Status&(8|64|128|4096|2) != 0 {
		if err == nil {
			err = wire.ErrMalformed
		}
		return n.batchRewriteUnknown(items, err)
	}
	n.op.SetOK(ok)
	n.recordWarnings(ok)
	for i := range items {
		items[i].Status = Succeeded
		if failure := failures[i]; failure != nil {
			items[i].Status, items[i].Err = Failed, failure
		} else if len(items) == 1 {
			items[i].RecordResult(command.Result{Known: true, Affected: ok.AffectedRows, ID: ok.InsertID})
		}
	}
	return nil
}

func (n *native) readBatchArrayPacket(ctx context.Context, budget *int64) ([]byte, error) {
	data, err := n.op.Packets().ReadMessage(ctx)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > n.op.Config().Limits.MaxMessageBytes-*budget {
		return nil, wire.ErrMessageTooLarge
	}
	*budget += int64(len(data))
	if len(data) > 0 && data[0] == 255 {
		return nil, command.DecodeServerError(data)
	}
	return data, nil
}

func (n *native) readBatchArrayFailures(ctx context.Context, data []byte, count int, budget *int64) (map[int]error, error) {
	columns, err := wire.DecodeColumnCount(data, 3)
	if err != nil || columns != 3 {
		return nil, wire.ErrMalformed
	}
	metadata := make([]wire.Column, 3)
	for i := range metadata {
		data, err = n.readBatchArrayPacket(ctx, budget)
		if err != nil {
			return nil, err
		}
		metadata[i], err = valuecodec.DecodeColumn(data, n.op.Config().Session, n.op.Protocol().NewMetadata)
		if err != nil {
			return nil, err
		}
	}
	// This native response always sends a legacy EOF, including with deprecated EOF negotiated.
	data, err = n.readBatchArrayPacket(ctx, budget)
	if err != nil {
		return nil, err
	}
	eof, err := wire.DecodeEOF(data)
	if err != nil || eof.Status&8 == 0 || metadata[0].InOut|metadata[1].InOut|metadata[2].InOut != 0 || metadata[0].Name != "sql_no" || metadata[0].Type != 8 || metadata[1].Name != "error_code" || metadata[1].Type != 8 || metadata[2].Name != "error_message" || metadata[2].Type != 253 {
		return nil, wire.ErrMalformed
	}
	failures := make(map[int]error)
	for {
		data, err = n.readBatchArrayPacket(ctx, budget)
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
		position, failure, decodeErr := n.decodeBatchArrayFailure(data, metadata, count)
		if decodeErr != nil || len(failures) >= count || failures[position] != nil {
			return nil, wire.ErrMalformed
		}
		failures[position] = failure
	}
}

func (n *native) decodeBatchArrayFailure(data []byte, metadata []wire.Column, count int) (int, *dberrors.Error, error) {
	fields, err := wire.DecodeBinaryRow(data, metadata)
	if err != nil || len(fields[0]) != 8 || len(fields[1]) != 8 || fields[2] == nil {
		return 0, nil, wire.ErrMalformed
	}
	position, code := binary.LittleEndian.Uint64(fields[0]), binary.LittleEndian.Uint64(fields[1])
	if position >= uint64(count) || code == 0 || code > math.MaxUint16 {
		return 0, nil, wire.ErrMalformed
	}
	message, err := valuecodec.DecodeRowValue(metadata[2], true, fields[2], n.op.Protocol().Mode, n.op.Config().Session)
	text, valid := message.(string)
	if err != nil || !valid {
		return 0, nil, wire.ErrMalformed
	}
	return int(position), &dberrors.Error{Code: uint32(code), Message: text}, nil
}
