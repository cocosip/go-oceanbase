// Package cursor owns explicit cursor state and protocol operations under a session lease.
package cursor

import (
	"context"
	"database/sql/driver"
	"math"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Mode selects a native forward cursor, bounded snapshot, or sensitive Oracle cursor.
type Mode uint8

// Explicit cursor modes.
const (
	Forward Mode = iota
	Static
	Sensitive
)

// Options controls cursor membership and snapshot limits.
type Options struct {
	Mode          Mode
	Updatable     bool
	MaxCacheBytes int64
}

// Origin identifies the reference point for a fetch position.
type Origin uint8

// Absolute positions are one-based; zero is before the first row.
const (
	Next Origin = iota
	Absolute
	Relative
)

// Position identifies the first row of the requested batch.
type Position struct {
	Origin Origin
	Offset int64
}

// BusyToken serializes public cursor operations, including the lifetime of a returned batch.
type BusyToken struct{ busy atomic.Bool }

// Begin checks cancellation before claiming an idle cursor token.
func (b *BusyToken) Begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.busy.CompareAndSwap(false, true) {
		return dberrors.ErrCursorBusy
	}
	return nil
}

// Release returns a cursor token after an operation or batch close.
func (b *BusyToken) Release() {
	if b != nil {
		b.busy.Store(false)
	}
}

type batchRows struct {
	*rowset.Rows
	busy *BusyToken
}

func (r *batchRows) Close() error { defer r.busy.Release(); return r.Rows.Close() }

type record struct {
	refID        uint32
	columns      []wire.Column
	options      Options
	position     int64
	last, offset bool
	cache        [][]driver.Value
	cacheBytes   int64
	identity     *catalog.CursorIdentity
	statement    *statement.Statement
	parent       *statement.Use
	scope        session.StatementScope
}

// Manager owns concrete cursor records; session retains only resource lifetime metadata.
type Manager struct {
	records map[session.ResourceRef]*record
}

// ValidateFetch checks public fetch bounds before a busy token or operation is acquired.
func ValidateFetch(pos Position, count int) error {
	if count < 1 || uint64(count) > math.MaxUint32 || pos.Origin > Relative || pos.Origin == Next && pos.Offset != 0 || pos.Origin == Absolute && pos.Offset < 0 {
		return dberrors.ErrCursor
	}
	return nil
}

func cursorStart(current int64, pos Position) (int64, error) {
	offset := pos.Offset
	switch pos.Origin {
	case Absolute:
		return offset, nil
	case Next:
		offset = 1
	}
	if offset > 0 && current > math.MaxInt64-offset || offset < 0 && offset < -current {
		return 0, dberrors.ErrCursor
	}
	return current + offset, nil
}

func staticCursorRows(cur *record, pos Position, count int) ([][]driver.Value, error) {
	start, err := cursorStart(cur.position, pos)
	if err != nil {
		return nil, err
	}
	if start == 0 {
		cur.position = 0
		return nil, nil
	}
	if start > int64(len(cur.cache)) {
		cur.position = int64(len(cur.cache)) + 1
		return nil, nil
	}
	n := min(int64(count), int64(len(cur.cache))-start+1)
	cur.position = start + n - 1
	return cur.cache[start-1 : start-1+n], nil
}
