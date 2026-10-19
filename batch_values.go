package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Only a plain INSERT INTO identifier [(columns)] VALUES (?,...) can be merged.
func batchValuesPlan(query string, noBackslash bool) (string, bindPlan, bool) {
	tokens, err := lexSQL(query, ModeMySQL, noBackslash, false)
	if err != nil || len(tokens) < 7 {
		return "", bindPlan{}, false
	}
	word := func(i int, w string) bool {
		return i < len(tokens) && tokens[i].kind == 'w' && strings.EqualFold(query[tokens[i].start:tokens[i].end], w)
	}
	ident := func(t sqlToken) bool {
		return t.kind == 'w' && (query[t.start] < '0' || query[t.start] > '9') || t.kind == 'q' && query[t.start] == '`'
	}
	if !word(0, "INSERT") || !word(1, "INTO") || !ident(tokens[2]) {
		return "", bindPlan{}, false
	}
	i := 3
	if tokens[i].kind == '.' {
		i++
		if !ident(tokens[i]) {
			return "", bindPlan{}, false
		}
		i++
	}
	if tokens[i].kind == '(' {
		i = batchValuesListEnd(tokens, i, ident)
	}
	if i < 0 || !word(i, "VALUES") {
		return "", bindPlan{}, false
	}
	i++
	start := i
	i = batchValuesListEnd(tokens, i, func(t sqlToken) bool { return t.kind == '?' })
	if i != len(tokens) {
		return "", bindPlan{}, false
	}
	plan, err := buildBindPlanEscapes(query[tokens[start].start:tokens[i-1].end], ModeMySQL, noBackslash)
	return query[:tokens[start].start], plan, err == nil
}

func batchValuesListEnd(tokens []sqlToken, i int, valid func(sqlToken) bool) int {
	if i >= len(tokens) || tokens[i].kind != '(' {
		return -1
	}
	i++
	for {
		if i >= len(tokens) || !valid(tokens[i]) {
			return -1
		}
		i++
		if i >= len(tokens) {
			return -1
		}
		if tokens[i].kind == ')' {
			return i + 1
		}
		if tokens[i].kind != ',' {
			return -1
		}
		i++
	}
}

func (c *conn) tryBatchMultiValues(ctx context.Context, request batchRequest) (bool, error) {
	if request.options.Strategy != BatchMultiValues || len(request.rows) < 2 || request.options.ChunkRows < 2 || !batchScalarRows(request.rows) {
		return false, nil
	}
	if err := c.lock(ctx); err != nil {
		return true, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return true, driver.ErrBadConn
	}
	if c.session.mode != ModeMySQL {
		return false, nil
	}
	_, plan, ok := batchValuesPlan(request.query, c.session.ok.Status&512 != 0)
	if !ok || len(plan.slots) > request.options.ChunkParameters/2 {
		return false, nil
	}
	request.result.Strategy = BatchMultiValues
	c.clearDescriptors()
	if err := c.clearStatementsLocked(ctx); err != nil {
		request.result.Items[0].Err = err
		return true, batchFailure(request.result)
	}
	err := c.execBatchValuesLocked(ctx, request)
	cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if cleanupErr := c.cleanupResourcesLocked(cleanup, false); cleanupErr != nil {
		err = errors.Join(err, &BatchError{Index: -1, Completed: batchSucceeded(request.result), Err: cleanupErr})
	}
	return true, err
}

func (c *conn) execBatchValuesLocked(ctx context.Context, request batchRequest) error {
	for next := 0; next < len(request.rows); {
		if err := ctx.Err(); err != nil {
			request.result.Items[next].Err = err
			break
		}
		prefix, plan, ok := batchValuesPlan(request.query, c.session.ok.Status&512 != 0)
		if !ok {
			request.result.Items[next] = BatchItem{Status: BatchFailed, Err: ErrBind}
			break
		}
		encoded, err := c.encodeSQL(prefix)
		if err != nil {
			request.result.Items[next] = BatchItem{Status: BatchFailed, Err: err}
			break
		}
		start := next
		payload := append([]byte{3}, encoded...)
		for next < len(request.rows) && next-start < request.options.ChunkRows && len(plan.slots) <= request.options.ChunkParameters-(next-start)*len(plan.slots) {
			tuple, encodeErr := c.encodeBatchRewriteLocked(plan, request.rows[next], request.options.ChunkParameters)
			if encodeErr != nil {
				if next == start {
					request.result.Items[next] = BatchItem{Status: BatchFailed, Err: encodeErr}
				}
				break
			}
			added := int64(len(tuple))
			if next > start {
				added++
			}
			if added > c.cfg.Limits.MaxMessageBytes-int64(len(payload)) {
				if next == start {
					request.result.Items[next] = BatchItem{Status: BatchFailed, Err: wire.ErrMessageTooLarge}
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
			if !request.options.ContinueOnError {
				break
			}
			next++
			continue
		}
		aggregate := []BatchItem{{}}
		err = c.runBatchRewriteLocked(ctx, payload, aggregate)
		item := aggregate[0]
		request.result.Chunks = append(request.result.Chunks, BatchChunk{Start: start, End: next, BatchItem: item})
		for i := start; i < next; i++ {
			request.result.Items[i] = item
			if next-start > 1 {
				request.result.Items[i].RowsAffected = 0
				request.result.Items[i].RowsAffectedKnown = false
				request.result.Items[i].InsertID = ""
				request.result.Items[i].InsertIDKnown = false
			}
		}
		if err != nil && (!request.options.ContinueOnError || c.closed.Load() || ctx.Err() != nil || item.Status != BatchFailed) {
			break
		}
	}
	return batchFailure(request.result)
}
