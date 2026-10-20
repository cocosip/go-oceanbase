package batch

import (
	"context"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (n *native) execBatchValuesLocked(ctx context.Context, request Request) error {
	for next := 0; next < len(request.Rows); {
		if err := ctx.Err(); err != nil {
			request.Result.Items[next].Err = err
			break
		}
		prefix, plan, ok := sqltext.BatchValuesPlan(request.Query, n.op.Protocol().OK.Status&512 != 0)
		if !ok {
			request.Result.Items[next] = Item{Status: Failed, Err: dberrors.ErrBind}
			break
		}
		encoded, err := n.encodeSQL(prefix)
		if err != nil {
			request.Result.Items[next] = Item{Status: Failed, Err: err}
			break
		}
		start := next
		payload := append([]byte{3}, encoded...)
		for next < len(request.Rows) && next-start < request.Options.ChunkRows && len(plan.Slots) <= request.Options.ChunkParameters-(next-start)*len(plan.Slots) {
			tuple, encodeErr := n.encodeBatchRewriteLocked(plan, request.Rows[next], request.Options.ChunkParameters)
			if encodeErr != nil {
				if next == start {
					request.Result.Items[next] = Item{Status: Failed, Err: encodeErr}
				}
				break
			}
			added := int64(len(tuple))
			if next > start {
				added++
			}
			if added > n.op.Config().Limits.MaxMessageBytes-int64(len(payload)) {
				if next == start {
					request.Result.Items[next] = Item{Status: Failed, Err: wire.ErrMessageTooLarge}
				}
				break
			}
			if next > start {
				payload = append(payload, ',')
			}
			payload = append(payload, tuple...)
			next++
		}
		if next == start {
			if !request.Options.ContinueOnError {
				break
			}
			next++
			continue
		}
		aggregate := []Item{{}}
		err = n.runBatchRewriteLocked(ctx, payload, aggregate)
		item := aggregate[0]
		request.Result.Chunks = append(request.Result.Chunks, Chunk{Start: start, End: next, Item: item})
		for i := start; i < next; i++ {
			request.Result.Items[i] = item
			if next-start > 1 {
				request.Result.Items[i].RowsAffected = 0
				request.Result.Items[i].RowsAffectedKnown = false
				request.Result.Items[i].InsertID = ""
				request.Result.Items[i].InsertIDKnown = false
			}
		}
		if err != nil && (!request.Options.ContinueOnError || n.op.Closed() || ctx.Err() != nil || item.Status != Failed) {
			break
		}
	}
	return Failure(request.Result)
}
