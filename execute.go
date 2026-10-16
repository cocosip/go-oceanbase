package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "" && len(args) == 1 {
		if request, ok := args[0].Value.(metadataRequest); ok {
			return c.queryMetadata(ctx, request.filter)
		}
		if request, ok := args[0].Value.(executionRequest); ok {
			return c.QueryContext(context.WithValue(ctx, executionContextKey{}, request.execution), request.query, request.args)
		}
	}
	if len(args) == 0 {
		return c.queryText(ctx, query)
	}
	if c.cfg.PrepareMode == PrepareClient {
		return (&clientStmt{conn: c, query: query}).queryRows(ctx, args)
	}
	prepared, err := c.prepareArguments(ctx, query, args)
	if err != nil {
		return nil, err
	}
	rows, err := prepared.query(ctx, args, true)
	if err != nil {
		_ = prepared.Close()
	}
	return rows, err
}
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "" && len(args) == 1 {
		if request, ok := args[0].Value.(savepointRequest); ok {
			return c.execSavepoint(ctx, request)
		}
	}
	if len(args) > 0 {
		if c.cfg.PrepareMode == PrepareClient {
			return (&clientStmt{conn: c, query: query}).ExecContext(ctx, args)
		}
		prepared, err := c.prepareArguments(ctx, query, args)
		if err != nil {
			return nil, err
		}
		result, err := prepared.exec(ctx, args, true)
		if err != nil {
			_ = prepared.Close()
		}
		return result, err
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
func (c *conn) prepareArguments(ctx context.Context, query string, args []driver.NamedValue) (*stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if len(args) > 65535 {
		return nil, fmt.Errorf("%w: too many arguments", ErrBind)
	}
	for i := range args {
		if c.session.mode == ModeMySQL && args[i].Name != "" {
			return nil, fmt.Errorf("%w: unexpected named argument", ErrBind)
		}
		value, err := convertArgument(args[i].Value)
		if err != nil {
			return nil, err
		}
		args[i].Value = value
	}
	if c.session.mode == ModeOracle {
		plan, err := buildBindPlan(query, c.session.mode)
		if err != nil {
			return nil, err
		}
		if _, err = bindArguments(plan, args); err != nil {
			return nil, err
		}
	}
	prepared, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return prepared.(*stmt), nil
}

func (c *conn) queryText(ctx context.Context, query string) (*textRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := c.encodeSQL(query)
	if err != nil {
		return nil, err
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		c.unlock()
		return nil, driver.ErrBadConn
	}
	return c.queryCommandLocked(ctx, append([]byte{3}, encoded...), false, nil)
}

func (c *conn) encodeSQL(query string) ([]byte, error) {
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, fmt.Errorf("%w: invalid SQL text", ErrBind)
	}
	name := c.cfg.Session.Charset
	if name == "" {
		name = charsetUTF8MB4
	}
	data, err := charset.Encode(name, query)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	return data, nil
}

func (c *conn) startRows(ctx context.Context, binary bool, auto *stmt) *textRows {
	c.responseActive = true
	r := &textRows{conn: c, ctx: ctx, done: true, binary: binary, autoStmt: auto}
	if c.activeTx != nil && c.activeTx.ctx.Done() != nil {
		txctx := c.activeTx.ctx
		op, cancel := context.WithCancelCause(ctx)
		stop := context.AfterFunc(txctx, func() { cancel(context.Cause(txctx)) })
		if txctx.Err() != nil {
			cancel(context.Cause(txctx))
		}
		r.stopTx = func() { stop(); cancel(nil) }
		ctx = op
		r.ctx = ctx
	}
	if ctx.Done() != nil {
		r.cancelDone = make(chan struct{})
		r.stopCancel = context.AfterFunc(ctx, func() { c.discard(); close(r.cancelDone) })
	}
	return r
}
func (c *conn) queryCommandLocked(ctx context.Context, payload []byte, binary bool, auto *stmt) (*textRows, error) {
	return c.queryCommandOwnedLocked(ctx, payload, binary, auto, false)
}

// keepGate lets an internal query finish without releasing its caller's operation gate.
func (c *conn) queryCommandOwnedLocked(ctx context.Context, payload []byte, binary bool, auto *stmt, keepGate bool) (*textRows, error) {
	r := c.startRows(ctx, binary, auto)
	r.keepGate = keepGate
	ctx = r.ctx
	r.execution, _ = ctx.Value(executionContextKey{}).(*Execution)
	c.session.packets.ResetSequence()
	if err := c.session.packets.WriteMessage(ctx, payload); err != nil {
		// A rejected pre-send packet does not poison the physical session.
		broken := !errorsPreSend(err)
		if broken {
			r.execution.begin()
		}
		return nil, r.fail(err, broken)
	}
	r.execution.begin()
	if err := r.advance(ctx); err != nil {
		return nil, r.fail(err, true)
	}
	if r.err != nil {
		return nil, r.err
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
