package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func batchMultiCandidate(request batchRequest) bool {
	if !request.options.ContinueOnError || len(request.rows) < 2 || request.options.Strategy != BatchAuto && request.options.Strategy != BatchMultiSend {
		return false
	}
	for _, row := range request.rows {
		for _, value := range row {
			if named, ok := value.(sql.NamedArg); ok {
				value = named.Value
			}
			switch value.(type) {
			case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string, []byte, time.Time, Number, Raw, NString, RowID, TimestampTZ, TimestampLTZ, IntervalYM, IntervalDS:
			default:
				return false
			}
		}
	}
	return true
}

func (c *conn) batchParametersValid(request batchRequest) bool {
	if c.session.mode == ModeOracle {
		plan, err := buildBindPlan(request.query, c.session.mode)
		if err != nil || len(plan.slots) > request.options.ChunkParameters {
			return false
		}
	}
	for _, row := range request.rows {
		if len(row) > request.options.ChunkParameters {
			return false
		}
		values, err := executionArguments(row)
		if err != nil || c.validatePrepareArguments(request.query, values) != nil {
			return false
		}
	}
	return true
}

// Multiple statement responses, outputs and session-changing SQL cannot enter this pipeline.
func batchDML(query string, mode Mode, noBackslash bool) bool {
	for _, ansi := range []bool{false, true} {
		tokens, err := lexSQL(query, mode, noBackslash, ansi)
		if err != nil || len(tokens) == 0 || tokens[0].kind != 'w' {
			return false
		}
		switch strings.ToUpper(query[tokens[0].start:tokens[0].end]) {
		case "INSERT", "UPDATE", "DELETE", "REPLACE":
		default:
			return false
		}
		for _, token := range tokens {
			if token.kind == ';' || token.kind == 'w' && strings.EqualFold(query[token.start:token.end], "RETURNING") {
				return false
			}
		}
	}
	return true
}

func (c *conn) execBatchMultiLocked(ctx context.Context, request batchRequest, prepared *stmt) (err error) {
	request.result.Strategy = BatchMultiSend
	r := c.startRows(ctx, true, nil)
	r.keepGate = true
	ctx = r.ctx
	defer func() {
		r.err = err
		r.release()
		if err == nil && r.err != nil {
			err = &BatchError{Index: -1, Completed: batchSucceeded(request.result), Err: r.err}
		} else {
			err = r.err
		}
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		cleanupErr := errors.Join(prepared.closeLocked(cleanup), c.cleanupResourcesLocked(cleanup, false))
		if cleanupErr != nil {
			err = errors.Join(err, &BatchError{Index: -1, Completed: batchSucceeded(request.result), Err: cleanupErr})
		}
	}()
	for next := 0; next < len(request.rows); {
		if cause := ctx.Err(); cause != nil {
			request.result.Items[next].Err = cause
			break
		}
		var payloads [][]byte
		var indices []int
		var bytes int64
		params := 0
		for next < len(request.rows) && len(payloads) < request.options.ChunkRows {
			width := len(prepared.plan.slots)
			if len(payloads) > 0 && width > request.options.ChunkParameters-params {
				break
			}
			payload, encodeErr := c.encodeBatchItemLocked(ctx, prepared, request.rows[next], request.options.ChunkParameters)
			if encodeErr != nil {
				request.result.Items[next] = BatchItem{Status: BatchFailed, Err: encodeErr}
				next++
				continue
			}
			if int64(len(payload)) > c.cfg.Limits.MaxMessageBytes-bytes && len(payloads) > 0 {
				break
			}
			bytes += int64(len(payload))
			params += width
			payloads = append(payloads, payload)
			indices = append(indices, next)
			next++
		}
		if len(payloads) == 0 {
			continue
		}
		if !c.runBatchPipelineLocked(ctx, request.result, payloads, indices) {
			break
		}
	}
	return batchFailure(request.result)
}

func (c *conn) encodeBatchItemLocked(ctx context.Context, prepared *stmt, args []any, maxParams int) ([]byte, error) {
	if len(args) > maxParams || len(prepared.plan.slots) > maxParams {
		return nil, fmt.Errorf("%w: batch parameter limit", ErrBind)
	}
	values, err := executionArguments(args)
	if err != nil {
		return nil, err
	}
	params, err := c.cursorParametersLocked(ctx, prepared.plan, values)
	if err != nil {
		return nil, err
	}
	query, err := c.encodeSQL(prepared.plan.sql)
	if err != nil {
		return nil, err
	}
	return wire.EncodeStmtExecuteOptions(prepared.id, params, c.cfg.Limits.MaxMessageBytes, c.executeOptions(query, false))
}

func (c *conn) runBatchPipelineLocked(ctx context.Context, result *BatchResult, payloads [][]byte, indices []int) bool {
	writes, err := c.session.packets.Pipeline(ctx, payloads, func(i int) error {
		data, e := c.session.packets.ReadMessage(ctx)
		if e != nil {
			return e
		}
		item := &result.Items[indices[i]]
		if len(data) > 0 && data[0] == 255 {
			e = decodeServerError(data)
			if e == wire.ErrMalformed {
				return e
			}
			*item = BatchItem{Status: BatchFailed, Started: true, Err: e}
			return nil
		}
		ok, e := wire.DecodeOK(data, c.session.capabilities)
		if e != nil {
			return e
		}
		if ok.Status&8 != 0 {
			return wire.ErrMalformed
		}
		c.observeResourceStatus(ok.Status)
		c.session.ok = ok
		*item = BatchItem{Status: BatchSucceeded, Started: true}
		if ok.AffectedRows <= math.MaxInt64 {
			item.RowsAffected = int64(ok.AffectedRows)
			item.RowsAffectedKnown = true
		}
		return nil
	})
	if err == nil {
		return true
	}
	c.discard()
	found := false
	for i, index := range indices {
		item := &result.Items[index]
		if item.Status != BatchNotExecuted {
			continue
		}
		if i < len(writes) && writes[i].Attempted {
			*item = BatchItem{Status: BatchUnknown, Started: true, Err: errors.Join(err, writes[i].Err)}
			found = true
		}
	}
	if !found {
		for _, index := range indices {
			if result.Items[index].Status == BatchNotExecuted {
				result.Items[index].Err = err
				return false
			}
		}
		// Complete acknowledgments can precede a local transport failure.
		item := &result.Items[indices[len(indices)-1]]
		item.Err = errors.Join(item.Err, err)
	}
	return false
}

func batchSucceeded(result *BatchResult) int {
	n := 0
	for _, item := range result.Items {
		if item.Status == BatchSucceeded {
			n++
		}
	}
	return n
}
