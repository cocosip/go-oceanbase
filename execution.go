package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/internal/execution"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	coresession "github.com/cocosip/go-oceanbase/internal/session"
	"sync"
	"sync/atomic"
)

// Session is a standard SQL session. Resource operations require a non-nil *sql.Conn or *sql.Tx.
type Session interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ResultKind identifies a server-observed result, preserving its execution order.
type ResultKind uint8

// Result kinds; command results do not become synthetic rowsets.
const (
	ResultRows ResultKind = iota
	ResultCommand
	ResultFailure
)

// ResultColumn preserves the server's column label and origin, decoded to UTF-8.
// Empty origin fields mean not supplied; they are never inferred from labels or SQL.
// Name is the result label; Table may be an alias. OriginalName and OriginalTable identify origins.
type ResultColumn struct {
	Catalog, Schema, Table, OriginalTable, Name, OriginalName string
}

// ResultInfo describes one observable result. Complete means its response boundary was consumed.
type ResultInfo struct {
	Kind              ResultKind
	RowSetIndex       int
	RowsAffected      int64
	RowsAffectedKnown bool
	InsertID          string
	InsertIDKnown     bool
	Complete          bool
	Err               error
	Warnings          WarningSummary
	// Columns is populated only for row results after their complete metadata was read.
	Columns []ResultColumn
}

// ExecutionSnapshot is an independent, concurrent-safe view of execution progress.
type ExecutionSnapshot struct {
	Results []ResultInfo
	Done    bool
	Err     error
}

// Execution owns standard Rows and records command and row results as they are consumed.
// Snapshot may be called concurrently; row iteration follows database/sql's contract.
type Execution struct {
	session  Session
	mu       sync.RWMutex
	rows     *sql.Rows
	recorder execution.Recorder
}

type executionRequest struct {
	query     string
	args      []driver.NamedValue
	execution *Execution
}
type resourceHandle struct {
	session Session
	ref     coresession.ResourceRef
	closed  atomic.Bool
}

type resourceRequest struct{ handle *resourceHandle }

func (h *resourceHandle) close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if !validSession(h.session) {
		return ErrResource
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.closed.Swap(true) {
		return nil
	}
	_, err := h.session.ExecContext(ctx, "", resourceRequest{handle: h})
	if errors.Is(err, ErrResource) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	if err != nil {
		h.closed.Store(false)
	}
	return err
}

// Execute runs SQL on a pinned session and retains partial results after a sent operation fails.
// Callers must consume or close its Rows, including command-only executions.
func Execute(ctx context.Context, session Session, query string, args ...any) (*Execution, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("oceanbase: a non-nil *sql.Conn or *sql.Tx is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := executionArguments(args)
	if err != nil {
		return nil, err
	}
	e := &Execution{session: session}
	rows, err := session.QueryContext(ctx, "", executionRequest{query: query, args: values, execution: e})
	e.mu.Lock()
	e.rows = rows
	e.mu.Unlock()
	if err != nil && !e.recorder.Started() {
		return nil, err
	}
	return e, err
}

func validSession(session Session) bool {
	switch s := session.(type) {
	case *sql.Conn:
		return s != nil
	case *sql.Tx:
		return s != nil
	default:
		return false
	}
}
func executionArguments(args []any) ([]driver.NamedValue, error) {
	return parameters.NamedArguments(args)
}

// Rows returns the same standard Rows on every call; it is nil after an initial query error.
func (e *Execution) Rows() *sql.Rows { e.mu.RLock(); defer e.mu.RUnlock(); return e.rows }

// Snapshot observes progress without reading the network or returning mutable internal storage.
func (e *Execution) Snapshot() ExecutionSnapshot {
	s := e.recorder.Snapshot()
	result := ExecutionSnapshot{Done: s.Done, Err: s.Err, Results: make([]ResultInfo, len(s.Results))}
	for i, item := range s.Results {
		info := ResultInfo{Kind: ResultKind(item.Kind), RowSetIndex: item.RowSetIndex, RowsAffected: item.RowsAffected, RowsAffectedKnown: item.RowsAffectedKnown, InsertID: item.InsertID, InsertIDKnown: item.InsertIDKnown, Complete: item.Complete, Err: item.Err, Warnings: WarningSummary{Count: item.Warnings.Count, Known: item.Warnings.Known}, Columns: make([]ResultColumn, len(item.Columns))}
		for j, column := range item.Columns {
			info.Columns[j] = ResultColumn{Catalog: column.Catalog, Schema: column.Schema, Table: column.Table, OriginalTable: column.OriginalTable, Name: column.Name, OriginalName: column.OriginalName}
		}
		if item.Columns == nil {
			info.Columns = nil
		}
		result.Results[i] = info
	}
	if s.Results == nil {
		result.Results = nil
	}
	return result
}

// Close drains unread results within the connection's cleanup budget and preserves prior errors.
// Stop row iteration before calling Close; it also reconciles database/sql cancellation errors.
func (e *Execution) Close() error {
	var closeErr error
	if rows := e.Rows(); rows != nil {
		closeErr = rows.Close()
		if closeErr == nil {
			closeErr = rows.Err()
		}
	}
	e.recorder.Finish(closeErr)
	return e.Snapshot().Err
}
