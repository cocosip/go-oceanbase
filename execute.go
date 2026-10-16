package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	plan, values, err := c.bindQuery(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if len(values) != 0 {
		return nil, fmt.Errorf("%w: query parameters", ErrUnsupported)
	}
	return c.queryText(ctx, plan.query)
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	plan, values, err := c.bindQuery(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if len(values) != 0 {
		return nil, fmt.Errorf("%w: execution parameters", ErrUnsupported)
	}
	rows, err := c.queryText(ctx, plan.query)
	if err != nil {
		return nil, err
	}
	if err = rows.drainAll(ctx); err != nil {
		return nil, err
	}
	return rows.last, nil
}

func (c *conn) bindQuery(ctx context.Context, query string, args []driver.NamedValue) (bindPlan, []boundValue, error) {
	if err := ctx.Err(); err != nil {
		return bindPlan{}, nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return bindPlan{}, nil, wire.ErrMessageTooLarge
	}
	if len(args) > 65535 {
		return bindPlan{}, nil, fmt.Errorf("%w: too many arguments", ErrBind)
	}
	converted := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		v, err := convertArgument(arg.Value)
		if err != nil {
			return bindPlan{}, nil, err
		}
		converted[i] = arg
		converted[i].Value = v
	}
	// Ordinary SQL remains native: the server parses compound statements and session quoting.
	if len(args) == 0 {
		return bindPlan{query: query, sql: query, mode: c.session.mode}, nil, nil
	}
	if err := c.lock(ctx); err != nil {
		return bindPlan{}, nil, err
	}
	noBackslash := c.session.ok.Status&512 != 0
	c.unlock()
	plan, err := buildBindPlanEscapes(query, c.session.mode, noBackslash)
	if err != nil {
		return bindPlan{}, nil, err
	}
	values, err := bindArguments(plan, converted)
	return plan, values, err
}

func (c *conn) queryText(ctx context.Context, query string) (*textRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		c.unlock()
		return nil, driver.ErrBadConn
	}
	r := &textRows{conn: c, ctx: ctx, done: true}
	if ctx.Done() != nil {
		r.cancelDone = make(chan struct{})
		r.stopCancel = context.AfterFunc(ctx, func() {
			c.discard()
			close(r.cancelDone)
		})
	}
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
