package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("%w: query parameters", ErrUnsupported)
	}
	return c.queryText(ctx, query)
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("%w: execution parameters", ErrUnsupported)
	}
	rows, err := c.queryText(ctx, query)
	if err != nil {
		return nil, err
	}
	if err = rows.drainAll(ctx); err != nil {
		return nil, err
	}
	return rows.last, nil
}

func (c *conn) queryText(ctx context.Context, query string) (*textRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, driver.ErrBadConn
	}
	r := &textRows{conn: c, ctx: ctx, done: true}
	c.session.packets.ResetSequence()
	if err := c.session.packets.WriteMessage(ctx, append([]byte{3}, query...)); err != nil {
		// A rejected pre-send packet does not poison the physical session.
		broken := !errorsPreSend(err)
		return nil, r.fail(err, broken)
	}
	if err := r.advance(ctx); err != nil {
		return nil, r.fail(err, true)
	}
	return r, nil
}

func errorsPreSend(err error) bool {
	return err == wire.ErrMessageTooLarge || err == context.Canceled || err == context.DeadlineExceeded
}

var (
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
)
