package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
)

// Execer is implemented by sql.DB, sql.Conn and sql.Tx.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// BatchStrategy is a preferred strategy; unsafe or unavailable optimizations fall back to sequential.
type BatchStrategy uint8

// Batch execution strategies. BatchResult reports the strategy actually used.
const (
	BatchAuto BatchStrategy = iota
	BatchSequential
	BatchMultiSend
	BatchRewrite
	BatchBulk
	BatchArrayBinding
)

// BatchOptions controls error handling and bounds each encoded chunk. Zero limits use defaults.
type BatchOptions struct {
	ContinueOnError bool
	Strategy        BatchStrategy
	ChunkRows       int
	ChunkParameters int
}

// BatchStatus distinguishes unsent, acknowledged and uncertain operations.
type BatchStatus uint8

// Batch item outcomes never imply that the caller's transaction has committed.
// BatchFailed records an error, not a guarantee of zero database effects.
const (
	BatchNotExecuted BatchStatus = iota
	BatchSucceeded
	BatchFailed
	BatchUnknown
)

// BatchItem describes one input row; Started includes a possibly partial business-command write.
type BatchItem struct {
	Status            BatchStatus
	Started           bool
	RowsAffected      int64
	RowsAffectedKnown bool
	Err               error
}

// BatchResult keeps input order, including items left unsent after a failure.
type BatchResult struct {
	Items    []BatchItem
	Strategy BatchStrategy
}

// BatchError identifies the first input with an error and the number of confirmed successes.
// Index is -1 for a batch-level finalization error without an input index.
type BatchError struct {
	Index     int
	Completed int
	Err       error
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("oceanbase: batch failed at input %d (%d succeeded)", e.Index, e.Completed)
}
func (e *BatchError) Unwrap() error { return e.Err }

// ExecBatch runs on one physical session without starting or finishing a transaction.
func ExecBatch(ctx context.Context, exec Execer, query string, rows [][]any, opts BatchOptions) (BatchResult, error) {
	result := BatchResult{Items: make([]BatchItem, len(rows)), Strategy: BatchSequential}
	if opts.Strategy > BatchArrayBinding || opts.ChunkRows < 0 || opts.ChunkParameters < 0 {
		return result, &ConfigError{Field: "BatchOptions"}
	}
	if nilExecer(exec) {
		return result, fmt.Errorf("oceanbase: a non-nil Execer is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, nil
	}
	if opts.ChunkRows == 0 {
		opts.ChunkRows = 128
	}
	if opts.ChunkParameters == 0 {
		opts.ChunkParameters = 65535
	}
	if db, ok := exec.(*sql.DB); ok {
		pinned, err := db.Conn(ctx)
		if err != nil {
			return result, err
		}
		defer func() { _ = pinned.Close() }()
		exec = pinned
	}
	switch exec.(type) {
	case *sql.Conn, *sql.Tx:
		_, err := exec.ExecContext(ctx, "", batchRequest{query: query, rows: rows, options: opts, result: &result})
		return result, err
	default:
		err := runBatchSequential(ctx, rows, opts, &result, func(ctx context.Context, args []any, attempt *batchAttempt) (sql.Result, error) {
			attempt.started = true
			return exec.ExecContext(ctx, query, args...)
		})
		return result, err
	}
}

func nilExecer(exec Execer) bool {
	if exec == nil {
		return true
	}
	v := reflect.ValueOf(exec)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

type batchRequest struct {
	query   string
	rows    [][]any
	options BatchOptions
	result  *BatchResult
}

type batchContextKey struct{}
type batchAttempt struct {
	native, started, broken, acknowledged bool
	result                                result
}
type batchExec func(context.Context, []any, *batchAttempt) (sql.Result, error)

func (c *conn) execBatch(ctx context.Context, request batchRequest) (driver.Result, error) {
	var prepareErr error
	if batchMultiCandidate(request) && c.cfg.PrepareMode == PrepareServer {
		if err := c.lock(ctx); err != nil {
			return nil, err
		}
		if !c.session.packets.Framed() && batchDML(request.query, c.session.mode, c.session.ok.Status&512 != 0) && c.batchParametersValid(request) {
			var prepared *stmt
			prepared, prepareErr = c.prepareServerLocked(ctx, request.query)
			if prepareErr == nil {
				err := c.execBatchMultiLocked(ctx, request, prepared)
				c.unlock()
				return result{}, err
			}
		}
		c.unlock()
	}
	// shortcut: rewrite/bulk/array stay sequential until equivalent per-item effects are verified.
	err := runBatchSequential(ctx, request.rows, request.options, request.result, func(ctx context.Context, args []any, attempt *batchAttempt) (sql.Result, error) {
		attempt.native = true
		if prepareErr != nil {
			err := prepareErr
			prepareErr = nil
			attempt.broken = c.closed.Load()
			return nil, err
		}
		values, err := executionArguments(args)
		if err != nil {
			return nil, err
		}
		res, err := c.ExecContext(ctx, request.query, values)
		attempt.broken = c.closed.Load()
		return res, err
	})
	return result{}, err
}

func runBatchSequential(ctx context.Context, rows [][]any, opts BatchOptions, result *BatchResult, exec batchExec) error {
	for i, args := range rows {
		item := &result.Items[i]
		if err := ctx.Err(); err != nil {
			item.Err = err
			break
		}
		if len(args) > opts.ChunkParameters || len(args) > 65535 {
			item.Status, item.Err = BatchFailed, fmt.Errorf("%w: batch parameter limit", ErrBind)
			if !opts.ContinueOnError {
				break
			}
			continue
		}
		attempt := &batchAttempt{}
		res, err := exec(context.WithValue(ctx, batchContextKey{}, attempt), args, attempt)
		item.Started, item.Err = attempt.started, err
		if err == nil || attempt.acknowledged {
			item.Status = BatchSucceeded
			if attempt.acknowledged {
				res = attempt.result
			}
			if res != nil {
				n, countErr := res.RowsAffected()
				if countErr == nil && n >= 0 {
					item.RowsAffected, item.RowsAffectedKnown = n, true
				}
			}
			if err == nil {
				continue
			}
		}
		var server *Error
		definite := attempt.acknowledged || attempt.native && (!attempt.broken || !attempt.started) || errors.As(err, &server)
		if !attempt.acknowledged {
			item.Status = BatchFailed
			if !definite {
				item.Status = BatchUnknown
			}
		}
		if !opts.ContinueOnError || !definite || attempt.broken || ctx.Err() != nil || errors.Is(err, driver.ErrBadConn) {
			break
		}
	}
	return batchFailure(result)
}

func (r *textRows) acknowledgeBatch() {
	if r.keepGate || r.err != nil || !r.done || r.more {
		return
	}
	if batch, _ := r.ctx.Value(batchContextKey{}).(*batchAttempt); batch != nil {
		batch.acknowledged, batch.result = true, r.last
	}
}

func batchFailure(result *BatchResult) error {
	first, completed := -1, 0
	var causes []error
	for i, item := range result.Items {
		if item.Status == BatchSucceeded {
			completed++
		}
		if item.Err != nil {
			if first < 0 {
				first = i
			}
			causes = append(causes, item.Err)
		}
	}
	if first < 0 {
		return nil
	}
	return &BatchError{Index: first, Completed: completed, Err: errors.Join(causes...)}
}

func (r *textRows) beginExecution() {
	r.execution.begin()
	if !r.keepGate {
		r.conn.lastWarnings = WarningSummary{}
		r.conn.warningsCleared = false
		if batch, _ := r.ctx.Value(batchContextKey{}).(*batchAttempt); batch != nil {
			batch.started = true
		}
	}
}
