package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/batch"
	"github.com/cocosip/go-oceanbase/internal/command"
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
	BatchMultiValues
)

// BatchOptions controls error handling and bounds each encoded chunk. Zero limits use defaults.
type BatchOptions struct {
	ContinueOnError bool
	Strategy        BatchStrategy
	ChunkRows       int
	ChunkParameters int
}

// BatchStatus distinguishes unexecuted, acknowledged and uncertain operations.
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
// A rewritten command can be sent but leave later items NotExecuted after a server error.
type BatchItem struct {
	Status            BatchStatus
	Started           bool
	RowsAffected      int64
	RowsAffectedKnown bool
	Err               error
	InsertID          string
	InsertIDKnown     bool
}

// BatchResult keeps input order, including items left unsent after a failure.
type BatchResult struct {
	Items    []BatchItem
	Strategy BatchStrategy
	Chunks   []BatchChunk
}

// BatchChunk reports the server's aggregate result for a multi-VALUES command.
// Start is inclusive and End is exclusive; no per-row counts or IDs are inferred.
type BatchChunk struct {
	Start, End int
	BatchItem
}

// BatchError identifies the first failed input and confirmed successes.
type BatchError = dberrors.BatchError

// ExecBatch runs on one physical session without starting or finishing a transaction.
func ExecBatch(ctx context.Context, exec Execer, query string, rows [][]any, opts BatchOptions) (BatchResult, error) {
	result := BatchResult{Items: make([]BatchItem, len(rows)), Strategy: BatchSequential}
	options, err := batch.NormalizeOptions(batchInternalOptions(opts))
	if err != nil {
		return result, err
	}
	if nilExecer(exec) {
		return result, fmt.Errorf("oceanbase: a non-nil Execer is required")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, nil
	}
	opts.ChunkRows, opts.ChunkParameters = options.ChunkRows, options.ChunkParameters
	if db, ok := exec.(*sql.DB); ok {
		pinned, pinErr := db.Conn(ctx)
		if pinErr != nil {
			return result, pinErr
		}
		defer func() { _ = pinned.Close() }()
		exec = pinned
	}
	switch exec.(type) {
	case *sql.Conn, *sql.Tx:
		_, err = exec.ExecContext(ctx, "", batchRequest{query: query, rows: rows, options: opts, result: &result})
		return result, err
	default:
		internalResult := batch.Result{}
		plan, planErr := batch.NewPlan(rows, options, &internalResult)
		if planErr != nil {
			return result, planErr
		}
		for {
			index, args, ok := plan.Next(ctx)
			if !ok {
				break
			}
			res, execErr := exec.ExecContext(ctx, query, args...)
			plan.Observe(index, batch.Observation{Started: true, Result: res, Err: execErr})
		}
		return batchPublicResult(internalResult), plan.Err()
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

func (c *conn) execBatch(ctx context.Context, request batchRequest) (driver.Result, error) {
	result, err := c.engine.Batch(ctx, request.query, request.rows, deferredBatchRows(request.rows), batchInternalOptions(request.options))
	if result != nil {
		*request.result = batchPublicResult(*result)
	}
	return command.Result{}, err
}

func batchInternalOptions(options BatchOptions) batch.Options {
	return batch.Options{ContinueOnError: options.ContinueOnError, Strategy: batch.Strategy(options.Strategy), ChunkRows: options.ChunkRows, ChunkParameters: options.ChunkParameters}
}

func batchPublicItem(item batch.Item) BatchItem {
	return BatchItem{Status: BatchStatus(item.Status), Started: item.Started, RowsAffected: item.RowsAffected, RowsAffectedKnown: item.RowsAffectedKnown, Err: item.Err, InsertID: item.InsertID, InsertIDKnown: item.InsertIDKnown}
}

func batchPublicResult(result batch.Result) BatchResult {
	public := BatchResult{Strategy: BatchStrategy(result.Strategy), Items: make([]BatchItem, len(result.Items))}
	for i, item := range result.Items {
		public.Items[i] = batchPublicItem(item)
	}
	if result.Chunks != nil {
		public.Chunks = make([]BatchChunk, len(result.Chunks))
		for i, chunk := range result.Chunks {
			public.Chunks[i] = BatchChunk{Start: chunk.Start, End: chunk.End, BatchItem: batchPublicItem(chunk.Item)}
		}
	}
	return public
}
