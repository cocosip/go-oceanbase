package batch

import (
	"context"
	"errors"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (n *native) execBatchMultiLocked(ctx context.Context, request Request, prepared *statement.Statement) (err error) {
	request.Result.Strategy = MultiSend
	boundary, beginErr := n.beginResponse(ctx, false)
	if beginErr != nil {
		request.Result.Items[0].Err = beginErr
		return Failure(request.Result)
	}
	defer func() {
		err = boundary.finish(err)
		if err != nil {
			var batchError *dberrors.BatchError
			if !errors.As(err, &batchError) {
				err = &dberrors.BatchError{Index: -1, Completed: SucceededCount(request.Result), Err: err}
			}
		}
		err = n.cleanup(prepared, err, request.Result)
	}()
	plan := prepared.Plan()
	query, queryErr := n.encodeSQL(plan.SQL)
	if queryErr != nil {
		for i := range request.Result.Items {
			request.Result.Items[i] = Item{Status: Failed, Err: queryErr}
		}
		return Failure(request.Result)
	}
	for next := 0; next < len(request.Rows); {
		if cause := ctx.Err(); cause != nil {
			request.Result.Items[next].Err = cause
			break
		}
		var payloads [][]byte
		var indices []int
		var bytes int64
		params := 0
		for next < len(request.Rows) && len(payloads) < request.Options.ChunkRows {
			width := len(plan.Slots)
			if len(payloads) > 0 && width > request.Options.ChunkParameters-params {
				break
			}
			payload, encodeErr := n.encodeBatchItemLocked(ctx, plan, prepared, request.Rows[next], request.Options.ChunkParameters, query)
			if encodeErr != nil {
				request.Result.Items[next] = Item{Status: Failed, Err: encodeErr}
				next++
				continue
			}
			if int64(len(payload)) > n.op.Config().Limits.MaxMessageBytes-bytes && len(payloads) > 0 {
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
		if !n.runBatchPipelineLocked(ctx, request.Result, payloads, indices) {
			break
		}
	}
	return Failure(request.Result)
}

func (n *native) encodeBatchItemLocked(ctx context.Context, plan sqltext.Plan, prepared *statement.Statement, args []any, maxParams int, query []byte) ([]byte, error) {
	if len(args) > maxParams || len(plan.Slots) > maxParams {
		return nil, batchLimitError()
	}
	params, err := n.encodeParameters(ctx, plan, args)
	if err != nil {
		return nil, err
	}
	return wire.EncodeStmtExecuteOptions(prepared.ID(), params, n.op.Config().Limits.MaxMessageBytes, statement.ExecuteOptions(n.op, query, false))
}

func (n *native) runBatchPipelineLocked(ctx context.Context, batch *Result, payloads [][]byte, indices []int) bool {
	if n.op.BeginCancellation() {
		defer n.op.EndCancellation(false)
	}
	// Each OK reports the server's warning count, so the batch outcome follows the last one.
	n.clearWarnings()
	writes, err := n.op.Packets().Pipeline(ctx, payloads, func(i int) error {
		data, e := n.op.Packets().ReadMessage(ctx)
		if e != nil {
			return e
		}
		item := &batch.Items[indices[i]]
		if len(data) > 0 && data[0] == 255 {
			e = command.DecodeServerError(data)
			if e == wire.ErrMalformed {
				return e
			}
			*item = Item{Status: Failed, Started: true, Err: e}
			return nil
		}
		ok, e := wire.DecodeOK(data, n.op.Protocol().Capabilities)
		if e != nil {
			return e
		}
		if ok.Status&wire.StatusMoreResults != 0 {
			return wire.ErrMalformed
		}
		n.op.SetOK(ok)
		n.recordWarnings(ok)
		*item = Item{Status: Succeeded, Started: true}
		item.RecordResult(command.Result{Known: true, Affected: ok.AffectedRows, ID: ok.InsertID})
		return nil
	})
	for _, write := range writes {
		if write.Attempted {
			n.outcome.Started = true
		}
	}
	if err == nil {
		return true
	}
	n.op.Discard()
	found := false
	for i, index := range indices {
		item := &batch.Items[index]
		if item.Status != NotExecuted {
			continue
		}
		if i < len(writes) && writes[i].Attempted {
			*item = Item{Status: Unknown, Started: true, Err: errors.Join(err, writes[i].Err)}
			found = true
		}
	}
	if !found {
		for _, index := range indices {
			if batch.Items[index].Status == NotExecuted {
				batch.Items[index].Err = err
				return false
			}
		}
		// Complete acknowledgments can precede a local transport failure.
		item := &batch.Items[indices[len(indices)-1]]
		item.Err = errors.Join(item.Err, err)
	}
	return false
}
