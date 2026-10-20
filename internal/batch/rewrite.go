package batch

import (
	"context"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (n *native) batchRewriteAllowedLocked(ctx context.Context) (bool, error) {
	// OceanBase can collapse a multi-statement request into one aggregate response.
	// This is a tenant parameter; every observer must explicitly report False.
	query := "SELECT MAX(COALESCE(LOWER(VALUE),'')!='false') FROM oceanbase.GV$OB_PARAMETERS WHERE NAME='ob_enable_batched_multi_statement'"
	values, _, err := command.QueryScalars(ctx, n.op, []byte(query), 1)
	if err != nil {
		var server *dberrors.Error
		if errors.As(err, &server) && server.Code == 1146 && !n.op.Closed() && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	return values[0].Valid && values[0].String == "0", nil
}

func (n *native) execBatchRewriteLocked(ctx context.Context, request Request) error {
	maxRows := min(request.Options.ChunkRows, n.op.Config().Limits.MaxResultItems)
	for next := 0; next < len(request.Rows); {
		if err := ctx.Err(); err != nil {
			request.Result.Items[next].Err = err
			break
		}
		if next > 0 && maxRows > 1 {
			allowed, err := n.batchRewriteAllowedLocked(ctx)
			if err != nil {
				request.Result.Items[next].Err = err
				break
			}
			if !allowed {
				// Already acknowledged items must not be replayed through the fallback.
				maxRows = 1
			}
		}
		if !sqltext.BatchDML(request.Query, n.op.Protocol().Mode, n.op.Protocol().OK.Status&512 != 0) {
			request.Result.Items[next] = Item{Status: Failed, Err: fmt.Errorf("%w: SQL mode changed batch statement boundaries", dberrors.ErrBind)}
			break
		}
		plan, err := sqltext.BuildBindPlanEscapes(request.Query, n.op.Protocol().Mode, n.op.Protocol().OK.Status&512 != 0)
		if err != nil {
			request.Result.Items[next] = Item{Status: Failed, Err: err}
			break
		}
		start := next
		payload := []byte{3}
		parameters := 0
		for next < len(request.Rows) && next-start < maxRows {
			if next > start && len(plan.Slots) > request.Options.ChunkParameters-parameters {
				break
			}
			query, encodeErr := n.encodeBatchRewriteLocked(plan, request.Rows[next], request.Options.ChunkParameters)
			if encodeErr != nil {
				// Flush a valid prefix before reporting an invalid later input.
				if next == start {
					request.Result.Items[next] = Item{Status: Failed, Err: encodeErr}
				}
				break
			}
			separator := ""
			if next > start {
				// End a trailing line comment before introducing the next statement.
				separator = "\n;\n"
			}
			if int64(len(query))+int64(len(separator)) > n.op.Config().Limits.MaxMessageBytes-int64(len(payload)) {
				break
			}
			payload = append(payload, separator...)
			payload = append(payload, query...)
			parameters += len(plan.Slots)
			next++
		}
		if next == start || n.runBatchRewriteLocked(ctx, payload, request.Result.Items[start:next]) != nil {
			break
		}
	}
	return Failure(request.Result)
}

func (n *native) encodeBatchRewriteLocked(plan sqltext.Plan, row []any, maxParams int) (string, error) {
	if len(row) > maxParams || len(row) > 65535 || len(plan.Slots) > maxParams {
		return "", batchLimitError()
	}
	args, err := rowArguments(row)
	if err != nil {
		return "", err
	}
	values, err := parameters.Bind(plan, args)
	if err != nil {
		return "", err
	}
	encoded, err := statement.Interpolate(n.op, plan, values)
	return string(encoded), err
}

func (n *native) runBatchRewriteLocked(ctx context.Context, payload []byte, items []Item) (err error) {
	boundary, beginErr := n.beginResponse(ctx, true)
	if beginErr != nil {
		items[0].Err = beginErr
		return beginErr
	}
	defer func() {
		err = boundary.finish(err)
		if err != nil {
			index := len(items) - 1
			for i := range items {
				if items[i].Err != nil {
					index = i
					break
				}
			}
			items[index].Err = err
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
	for i := range items {
		data, readErr := n.op.Packets().ReadMessage(ctx)
		if readErr != nil {
			return n.batchRewriteUnknown(items[i:], readErr)
		}
		if len(data) > 0 && data[0] == 255 {
			serverErr := command.DecodeServerError(data)
			var server *dberrors.Error
			if !errors.As(serverErr, &server) {
				return n.batchRewriteUnknown(items[i:], serverErr)
			}
			items[i].Status, items[i].Err = Failed, serverErr
			return serverErr
		}
		ok, decodeErr := wire.DecodeOK(data, n.op.Protocol().Capabilities)
		if decodeErr != nil {
			return n.batchRewriteUnknown(items[i:], decodeErr)
		}
		if (ok.Status&8 != 0) != (i < len(items)-1) {
			return n.batchRewriteUnknown(items[i:], wire.ErrMalformed)
		}
		n.op.SetOK(ok)
		n.recordWarnings(ok)
		items[i].Status = Succeeded
		items[i].RecordResult(command.Result{Known: true, Affected: ok.AffectedRows, ID: ok.InsertID})
	}
	return nil
}
