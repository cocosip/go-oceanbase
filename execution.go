package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/wire"
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
	mu       sync.RWMutex
	rows     *sql.Rows
	snapshot ExecutionSnapshot
	started  bool
	rowsets  int
}

type executionRequest struct {
	query     string
	args      []driver.NamedValue
	execution *Execution
}
type executionContextKey struct{}

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
	e := &Execution{}
	rows, err := session.QueryContext(ctx, "", executionRequest{query: query, args: values, execution: e})
	e.mu.Lock()
	e.rows = rows
	started := e.started
	e.mu.Unlock()
	if err != nil && !started {
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
	values := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		value := driver.NamedValue{Ordinal: i + 1, Value: arg}
		if named, ok := arg.(sql.NamedArg); ok {
			if named.Name != "" {
				r, _ := utf8.DecodeRuneInString(named.Name)
				if !unicode.IsLetter(r) {
					return nil, fmt.Errorf("%w: argument name", ErrBind)
				}
			}
			value.Name, value.Value = named.Name, named.Value
		}
		values[i] = value
	}
	return values, nil
}

// Rows returns the same standard Rows on every call; it is nil after an initial query error.
func (e *Execution) Rows() *sql.Rows { e.mu.RLock(); defer e.mu.RUnlock(); return e.rows }

// Snapshot observes progress without reading the network or returning mutable internal storage.
func (e *Execution) Snapshot() ExecutionSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := e.snapshot
	s.Results = slices.Clone(s.Results)
	return s
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
	e.finish(closeErr)
	return e.Snapshot().Err
}

func (e *Execution) begin() {
	if e != nil {
		e.mu.Lock()
		e.started = true
		e.mu.Unlock()
	}
}
func (e *Execution) add(info ResultInfo) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if info.Kind == ResultRows {
		info.RowSetIndex = e.rowsets
		e.rowsets++
	} else {
		info.RowSetIndex = -1
	}
	e.snapshot.Results = append(e.snapshot.Results, info)
}
func (e *Execution) command(ok wire.OK) error {
	if e == nil {
		return nil
	}
	info := ResultInfo{Kind: ResultCommand, RowsAffectedKnown: true, Complete: true}
	if ok.InsertID != 0 {
		info.InsertID = strconv.FormatUint(ok.InsertID, 10)
		info.InsertIDKnown = true
	}
	if ok.AffectedRows > math.MaxInt64 {
		info.RowsAffectedKnown = false
		info.Err = errCountOverflow
	} else {
		info.RowsAffected = int64(ok.AffectedRows)
	}
	e.add(info)
	return info.Err
}
func (e *Execution) rowDone() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := len(e.snapshot.Results); n > 0 && e.snapshot.Results[n-1].Kind == ResultRows {
		e.snapshot.Results[n-1].Complete = true
	}
}
func (e *Execution) finish(err error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil && e.snapshot.Err == nil {
		e.snapshot.Err = err
	}
	if n := len(e.snapshot.Results); err != nil && n > 0 && !e.snapshot.Results[n-1].Complete && e.snapshot.Results[n-1].Err == nil {
		e.snapshot.Results[n-1].Err = err
	}
	e.snapshot.Done = true
}
