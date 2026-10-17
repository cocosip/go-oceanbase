package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"sync/atomic"
)

// ErrCursor identifies an invalid cursor position, batch size or result shape.
var ErrCursor = errors.New("oceanbase: invalid cursor operation")

// ErrCursorBusy means a previous operation or SQL row batch must finish first.
var ErrCursorBusy = errors.New("oceanbase: cursor has an active operation or row batch")

// CursorMode selects server-forward, bounded static, or server-sensitive reading.
type CursorMode uint8

// Cursor reading modes. Static retains a bounded snapshot, never a sensitive view.
const (
	CursorForward CursorMode = iota
	CursorStatic
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
	busy   atomic.Bool
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
		values[i].Value, err = convertArgument(values[i].Value)
		if err != nil {
			return nil, err
		}
		if _, output := values[i].Value.(*outBinding); output {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.busy.CompareAndSwap(false, true) {
		return ErrCursorBusy
	}
	return nil
}

// Fetch returns one fully received, bounded standard SQL batch. Close or exhaust
// that batch before using the cursor again. Each fetch uses its own context.
func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error) {
	if count < 1 || uint64(count) > math.MaxUint32 || pos.Origin > CursorRelative || pos.Origin == CursorNext && pos.Offset != 0 || pos.Origin == CursorAbsolute && pos.Offset < 0 {
		return nil, ErrCursor
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	rows, err := c.handle.session.QueryContext(ctx, "", cursorFetchRequest{c, pos, count})
	if err != nil {
		c.busy.Store(false)
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
	defer c.busy.Store(false)
	return c.handle.close(context.Background())
}
