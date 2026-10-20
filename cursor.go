package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// ErrCursor identifies an invalid cursor position, batch size or result shape.
var ErrCursor = dberrors.ErrCursor

// ErrCursorBusy means a previous operation or SQL row batch must finish first.
var ErrCursorBusy = dberrors.ErrCursorBusy

// CursorMode selects server-forward, bounded static, or server-sensitive reading.
type CursorMode uint8

// Cursor reading modes. Static retains a bounded snapshot, never a sensitive view.
const (
	CursorForward CursorMode = iota
	CursorStatic
	// CursorSensitive retains native Oracle cursor membership/order and refreshes
	// fetched base-table columns by ROWID under the session's current isolation.
	// It requires a simple single-table SELECT. A deleted row invalidates the
	// cursor with ErrCursor and sql.ErrNoRows; the caller's session remains usable.
	CursorSensitive
)

// CursorOptions controls the explicit cursor, independently of ordinary sql.Rows.
type CursorOptions struct {
	Mode          CursorMode
	Updatable     bool
	MaxCacheBytes int64
}

// CursorOrigin identifies the reference point for a Fetch position.
type CursorOrigin uint8

// Cursor position origins. Absolute positions are one-based; zero is before first.
const (
	CursorNext CursorOrigin = iota
	CursorAbsolute
	CursorRelative
)

// CursorPosition describes the first row of a batch. Relative uses the last
// fetched row, including rows skipped by closing a batch early. Next requires Offset=0.
type CursorPosition struct {
	Origin CursorOrigin
	Offset int64
}

// Cursor owns a server statement on a caller-owned fixed SQL session.
type Cursor struct {
	handle *resourceHandle
	busy   cursor.BusyToken
}

type cursorOpenRequest struct {
	session Session
	query   string
	args    []driver.NamedValue
	options CursorOptions
	result  **resourceHandle
}

type cursorFetchRequest struct {
	cursor   *Cursor
	position CursorPosition
	count    int
}

// OpenCursor opens a server cursor on *sql.Conn or *sql.Tx. It never owns the session.
func OpenCursor(ctx context.Context, session Session, query string, opts CursorOptions, args ...any) (*Cursor, error) {
	if !validSession(session) {
		return nil, ErrResource
	}
	if opts.Mode > CursorSensitive || opts.MaxCacheBytes < 0 {
		return nil, ErrCursor
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := executionArguments(args)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i].Value, err = normalizeArgument(values[i].Value)
		if err != nil {
			return nil, err
		}
		if _, output := values[i].Value.(*call.Binding); output {
			return nil, ErrCursor
		}
	}
	var handle *resourceHandle
	_, err = session.ExecContext(ctx, "", cursorOpenRequest{session, query, values, opts, &handle})
	if err != nil {
		return nil, err
	}
	return &Cursor{handle: handle}, nil
}

func (c *Cursor) begin(ctx context.Context) error {
	if c == nil || c.handle == nil || c.handle.closed.Load() {
		return ErrResource
	}
	return c.busy.Begin(ctx)
}

// Fetch returns one fully received, bounded standard SQL batch. Close or exhaust
// that batch before using the cursor again. Each fetch uses its own context.
func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error) {
	if err := cursor.ValidateFetch(cursor.Position{Origin: cursor.Origin(pos.Origin), Offset: pos.Offset}, count); err != nil {
		return nil, err
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	rows, err := c.handle.session.QueryContext(ctx, "", cursorFetchRequest{c, pos, count})
	if err != nil {
		c.busy.Release()
	}
	return rows, err
}

// Close releases the server cursor and its statement without ending the session.
// An active batch must be closed first. Close is idempotent after success.
func (c *Cursor) Close() error {
	if c == nil || c.handle == nil || c.handle.closed.Load() {
		return nil
	}
	if err := c.begin(context.Background()); err != nil {
		return err
	}
	defer c.busy.Release()
	return c.handle.close(context.Background())
}

// ColumnChange assigns a value to one projected base column, using its dictionary name.
type ColumnChange struct {
	Name  string
	Value any
}

type cursorOperation uint8

const (
	cursorUpdate cursorOperation = iota
	cursorDelete
	cursorInsert
	cursorRefresh
)

type cursorMutationRequest struct {
	cursor    *Cursor
	operation cursorOperation
	key       map[string]any
	changes   []ColumnChange
}

// Update changes one row by its complete primary key in the caller's writable transaction.
// Static batches remain the original snapshot; Refresh reads current base-table values.
func (c *Cursor) Update(ctx context.Context, key map[string]any, changes ...ColumnChange) error {
	return c.mutate(ctx, cursorUpdate, key, changes)
}

// Delete removes one row by its complete primary key in the caller's writable transaction.
func (c *Cursor) Delete(ctx context.Context, key map[string]any) error {
	return c.mutate(ctx, cursorDelete, key, nil)
}

// Insert inserts one row with explicit projected columns, including the complete primary key.
func (c *Cursor) Insert(ctx context.Context, changes ...ColumnChange) error {
	return c.mutate(ctx, cursorInsert, nil, changes)
}

func (c *Cursor) mutate(ctx context.Context, op cursorOperation, key map[string]any, changes []ColumnChange) error {
	if err := c.begin(ctx); err != nil {
		return err
	}
	defer c.busy.Release()
	request, err := cursorRequest(c, op, key, changes)
	if err != nil {
		return err
	}
	_, err = c.handle.session.ExecContext(ctx, "", request)
	return err
}

// Refresh returns the current projected base-table row for a complete primary key.
// It does not reapply the original SELECT filter or replace a static snapshot.
func (c *Cursor) Refresh(ctx context.Context, key map[string]any) (*sql.Rows, error) {
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	request, err := cursorRequest(c, cursorRefresh, key, nil)
	if err != nil {
		c.busy.Release()
		return nil, err
	}
	rows, err := c.handle.session.QueryContext(ctx, "", request)
	if err != nil {
		c.busy.Release()
	}
	return rows, err
}

func cursorRequest(c *Cursor, op cursorOperation, key map[string]any, changes []ColumnChange) (cursorMutationRequest, error) {
	request := cursorMutationRequest{cursor: c, operation: op}
	if op != cursorInsert {
		normalized, err := normalizeBlobTarget(BlobTarget{Table: "t", Column: "c", Key: key})
		if err != nil {
			return request, errors.Join(ErrCursor, err)
		}
		request.key = normalized.Key
	}
	if (op == cursorUpdate || op == cursorInsert) && (len(changes) == 0 || len(changes) > command.MaxColumns) {
		return request, ErrCursor
	}
	for _, change := range changes {
		if !valueformat.Identifier(change.Name) {
			return request, ErrCursor
		}
		v, err := normalizeArgument(change.Value)
		if err != nil {
			return request, err
		}
		if _, out := v.(*call.Binding); out {
			return request, ErrCursor
		}
		request.changes = append(request.changes, ColumnChange{change.Name, v})
	}
	return request, nil
}

func (c *conn) execOpenCursor(ctx context.Context, request cursorOpenRequest) (driver.Result, error) {
	if !validSession(request.session) || request.result == nil {
		return nil, ErrResource
	}
	_, transaction := request.session.(*sql.Tx)
	ref, err := c.engine.OpenCursor(ctx, request.query, request.args, cursor.Options{Mode: cursor.Mode(request.options.Mode), Updatable: request.options.Updatable, MaxCacheBytes: request.options.MaxCacheBytes}, transaction)
	if err != nil {
		return nil, err
	}
	*request.result = &resourceHandle{session: request.session, ref: ref}
	return command.Result{Known: true}, nil
}

func (c *conn) queryCursorFetch(ctx context.Context, request cursorFetchRequest) (driver.Rows, error) {
	if request.cursor == nil || request.cursor.handle == nil || request.cursor.handle.closed.Load() {
		return nil, ErrResource
	}
	rows, retire, err := c.engine.FetchCursor(ctx, request.cursor.handle.ref, cursor.Position{Origin: cursor.Origin(request.position.Origin), Offset: request.position.Offset}, request.count, &request.cursor.busy)
	if retire {
		request.cursor.handle.closed.Store(true)
	}
	return rows, err
}

func internalCursorRequest(request cursorMutationRequest) cursor.Request {
	changes := make([]cursor.ColumnChange, len(request.changes))
	for i, change := range request.changes {
		changes[i] = cursor.ColumnChange{Name: change.Name, Value: change.Value}
	}
	return cursor.Request{Operation: cursor.Operation(request.operation), Key: request.key, Changes: changes}
}

func (c *conn) execCursorMutation(ctx context.Context, request cursorMutationRequest) (driver.Result, error) {
	if request.cursor == nil || request.cursor.handle == nil || request.cursor.handle.closed.Load() {
		return nil, ErrResource
	}
	_, transaction := request.cursor.handle.session.(*sql.Tx)
	result, retire, err := c.engine.MutateCursor(ctx, request.cursor.handle.ref, internalCursorRequest(request), transaction)
	if retire {
		request.cursor.handle.closed.Store(true)
	}
	return result, err
}

func (c *conn) queryCursorRefresh(ctx context.Context, request cursorMutationRequest) (driver.Rows, error) {
	if request.cursor == nil || request.cursor.handle == nil || request.cursor.handle.closed.Load() {
		return nil, ErrResource
	}
	_, transaction := request.cursor.handle.session.(*sql.Tx)
	rows, retire, err := c.engine.RefreshCursor(ctx, request.cursor.handle.ref, internalCursorRequest(request), transaction, &request.cursor.busy)
	if retire {
		request.cursor.handle.closed.Store(true)
	}
	return rows, err
}
