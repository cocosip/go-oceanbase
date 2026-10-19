package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Rewrite keeps independent statements: a VALUES merge would change partial-success semantics.
func (c *conn) tryBatchRewrite(ctx context.Context, request batchRequest) (bool, error) {
	if request.options.Strategy != BatchRewrite || request.options.ContinueOnError || len(request.rows) < 2 || request.options.ChunkRows < 2 || !c.cfg.AllowMultiStatements || !batchScalarRows(request.rows) {
		return false, nil
	}
	if err := c.lock(ctx); err != nil {
		return true, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return true, driver.ErrBadConn
	}
	if c.session.mode != ModeMySQL || c.session.capabilities&wire.ClientMultiStatements == 0 || !batchDML(request.query, c.session.mode, c.session.ok.Status&512 != 0) {
		return false, nil
	}
	plan, err := buildBindPlanEscapes(request.query, c.session.mode, c.session.ok.Status&512 != 0)
	if err != nil || len(plan.slots) > request.options.ChunkParameters/2 || c.cfg.Limits.MaxResultItems < 2 {
		return false, nil
	}
	allowed, err := c.batchRewriteAllowedLocked(ctx)
	if err != nil {
		request.result.Items[0].Err = err
		return true, batchFailure(request.result)
	}
	if !allowed {
		return false, nil
	}
	request.result.Strategy = BatchRewrite
	c.clearDescriptors()
	if err = c.clearStatementsLocked(ctx); err != nil {
		request.result.Items[0].Err = err
		return true, batchFailure(request.result)
	}
	err = c.execBatchRewriteLocked(ctx, request)
	cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if cleanupErr := c.cleanupResourcesLocked(cleanup, false); cleanupErr != nil {
		err = errors.Join(err, &BatchError{Index: -1, Completed: batchSucceeded(request.result), Err: cleanupErr})
	}
	return true, err
}

func (c *conn) batchRewriteAllowedLocked(ctx context.Context) (bool, error) {
	// OceanBase can collapse a multi-statement request into one aggregate response.
	values, _, err := c.sessionValuesLocked(ctx, []byte("SELECT @@SESSION.ob_enable_batched_multi_statement"), 1)
	if err != nil {
		var server *Error
		if errors.As(err, &server) && server.Code == 1193 && !c.closed.Load() && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	return values[0].Valid && (values[0].String == "0" || strings.EqualFold(values[0].String, "OFF")), nil
}

func (c *conn) execBatchRewriteLocked(ctx context.Context, request batchRequest) error {
	maxRows := min(request.options.ChunkRows, c.cfg.Limits.MaxResultItems)
	for next := 0; next < len(request.rows); {
		if err := ctx.Err(); err != nil {
			request.result.Items[next].Err = err
			break
		}
		if next > 0 && maxRows > 1 {
			allowed, err := c.batchRewriteAllowedLocked(ctx)
			if err != nil {
				request.result.Items[next].Err = err
				break
			}
			if !allowed {
				// Already acknowledged items must not be replayed through the fallback.
				maxRows = 1
			}
		}
		if !batchDML(request.query, c.session.mode, c.session.ok.Status&512 != 0) {
			request.result.Items[next] = BatchItem{Status: BatchFailed, Err: fmt.Errorf("%w: SQL mode changed batch statement boundaries", ErrBind)}
			break
		}
		plan, err := buildBindPlanEscapes(request.query, c.session.mode, c.session.ok.Status&512 != 0)
		if err != nil {
			request.result.Items[next] = BatchItem{Status: BatchFailed, Err: err}
			break
		}
		start := next
		payload := []byte{3}
		parameters := 0
		for next < len(request.rows) && next-start < maxRows {
			if next > start && len(plan.slots) > request.options.ChunkParameters-parameters {
				break
			}
			query, encodeErr := c.encodeBatchRewriteLocked(plan, request.rows[next], request.options.ChunkParameters)
			if encodeErr != nil {
				// Flush a valid prefix before reporting an invalid later input.
				if next == start {
					request.result.Items[next] = BatchItem{Status: BatchFailed, Err: encodeErr}
				}
				break
			}
			separator := ""
			if next > start {
				// End a trailing line comment before introducing the next statement.
				separator = "\n;\n"
			}
			if int64(len(query))+int64(len(separator)) > c.cfg.Limits.MaxMessageBytes-int64(len(payload)) {
				break
			}
			payload = append(payload, separator...)
			payload = append(payload, query...)
			parameters += len(plan.slots)
			next++
		}
		if next == start || c.runBatchRewriteLocked(ctx, payload, request.result.Items[start:next]) != nil {
			break
		}
	}
	return batchFailure(request.result)
}

func (c *conn) encodeBatchRewriteLocked(plan bindPlan, row []any, maxParams int) (string, error) {
	if len(row) > maxParams || len(row) > 65535 || len(plan.slots) > maxParams {
		return "", fmt.Errorf("%w: batch parameter limit", ErrBind)
	}
	args, err := executionArguments(row)
	if err != nil {
		return "", err
	}
	values, err := bindArguments(plan, args)
	if err != nil {
		return "", err
	}
	return c.interpolatePlan(plan, values)
}

func (c *conn) runBatchRewriteLocked(ctx context.Context, payload []byte, items []BatchItem) (err error) {
	r := c.startRows(ctx, false, nil)
	r.keepGate = true
	r.cancelExecution = c.beginCancelExecution()
	ctx = r.ctx
	defer func() {
		r.err = err
		r.release()
		if r.err != nil {
			index := len(items) - 1
			for i := range items {
				if items[i].Err != nil {
					index = i
					break
				}
			}
			items[index].Err = r.err
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
	for i := range items {
		data, readErr := c.session.packets.ReadMessage(ctx)
		if readErr != nil {
			return c.batchRewriteUnknown(items[i:], readErr)
		}
		if len(data) > 0 && data[0] == 255 {
			serverErr := decodeServerError(data)
			var server *Error
			if !errors.As(serverErr, &server) {
				return c.batchRewriteUnknown(items[i:], serverErr)
			}
			items[i].Status, items[i].Err = BatchFailed, serverErr
			return serverErr
		}
		ok, decodeErr := wire.DecodeOK(data, c.session.capabilities)
		if decodeErr != nil {
			return c.batchRewriteUnknown(items[i:], decodeErr)
		}
		if (ok.Status&8 != 0) != (i < len(items)-1) {
			return c.batchRewriteUnknown(items[i:], wire.ErrMalformed)
		}
		c.observeResourceStatus(ok.Status)
		c.session.ok = ok
		c.lastWarnings = WarningSummary{Count: ok.Warnings, Known: true}
		items[i].Status = BatchSucceeded
		items[i].recordResult(result{known: true, affected: ok.AffectedRows, id: ok.InsertID})
	}
	return nil
}

func (c *conn) batchRewriteUnknown(items []BatchItem, err error) error {
	c.discard()
	for i := range items {
		items[i] = BatchItem{Status: BatchUnknown, Started: true, Err: err}
	}
	return err
}
