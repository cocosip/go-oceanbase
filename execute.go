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
		if request, ok := args[0].Value.(cursorMutationRequest); ok {
			return c.queryCursorRefresh(ctx, request)
		}
		if request, ok := args[0].Value.(cursorFetchRequest); ok {
			return c.queryCursorFetch(ctx, request)
		}
		if request, ok := args[0].Value.(metadataRequest); ok {
			return c.queryMetadata(ctx, request.filter)
		}
		if request, ok := args[0].Value.(executionRequest); ok {
			return c.QueryContext(context.WithValue(ctx, executionContextKey{}, request.execution), request.query, request.args)
		}
	}
	if hasOutputArguments(args) {
		return c.queryOutputs(ctx, query, args, nil)
	}
	if len(args) == 0 {
		return c.queryText(ctx, query)
	}
	if c.cfg.PrepareMode == PrepareClient {
		return (&clientStmt{conn: c, query: query, epoch: c.statementEpoch}).queryRows(ctx, args)
	}
	prepared, err := c.prepareArguments(ctx, query, args)
	if err != nil {
		return nil, err
	}
	rows, err := prepared.query(ctx, args, true)
	if err != nil {
		state, _ := ctx.Value(lobOpenContextKey{}).(*lobOpenState)
		// Captured LOB rows close their automatic statement under the retained gate.
		if state == nil || !state.gate {
			_ = prepared.Close()
		}
	}
	return rows, err
}
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "" && len(args) == 1 {
		if request, ok := args[0].Value.(*xaRequest); ok {
			return c.execXA(ctx, request)
		}
		if request, ok := args[0].Value.(changeUserRequest); ok {
			return c.execChangeUser(ctx, request)
		}
		if request, ok := args[0].Value.(*sessionOperationRequest); ok {
			return c.execSessionOperation(ctx, request)
		}
		if request, ok := args[0].Value.(localInfileRequest); ok {
			return c.execLocalInfile(ctx, request)
		}
		if request, ok := args[0].Value.(batchRequest); ok {
			return c.execBatch(ctx, request)
		}
		if request, ok := args[0].Value.(cursorMutationRequest); ok {
			return c.execCursorMutation(ctx, request)
		}
		if request, ok := args[0].Value.(cursorOpenRequest); ok {
			return c.execOpenCursor(ctx, request)
		}
		if request, ok := args[0].Value.(lobOpenRequest); ok {
			return c.execOpenLOB(ctx, request)
		}
		if request, ok := args[0].Value.(tableBlobOpenRequest); ok {
			return c.execOpenTableBlob(ctx, request)
		}
		if request, ok := args[0].Value.(lobRequest); ok {
			return c.execLOB(ctx, request)
		}
		if request, ok := args[0].Value.(resourceRequest); ok {
			return c.execResource(ctx, request)
		}
		if request, ok := args[0].Value.(savepointRequest); ok {
			return c.execSavepoint(ctx, request)
		}
	}
	if hasOutputArguments(args) {
		rows, err := c.queryOutputs(ctx, query, args, nil)
		if err != nil {
			return nil, err
		}
		if err = rows.drainAll(ctx); err != nil {
			return nil, err
		}
		return rows.last, nil
	}
	if len(args) > 0 {
		if c.cfg.PrepareMode == PrepareClient {
			return (&clientStmt{conn: c, query: query, epoch: c.statementEpoch}).ExecContext(ctx, args)
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
	if err := c.validatePrepareArguments(query, args); err != nil {
		return nil, err
	}
	combined := c.usePrepareExecute()
	for _, arg := range args {
		if _, ok := arg.Value.(*LongData); ok {
			combined = false
		}
	}
	return c.prepareAutomatic(ctx, query, combined)
}

func (c *conn) validatePrepareArguments(query string, args []driver.NamedValue) error {
	if int64(utf8.RuneCountInString(query)) >= c.cfg.Limits.MaxMessageBytes {
		return wire.ErrMessageTooLarge
	}
	if len(args) > 65535 {
		return fmt.Errorf("%w: too many arguments", ErrBind)
	}
	if c.session.mode == ModeMySQL && hasNamedArguments(args) {
		if _, err := parseNamedRoutine(query, c.session.ok.Status&512 != 0); err != nil {
			return err
		}
	}
	for i := range args {
		value, err := convertArgument(args[i].Value)
		if err != nil {
			return err
		}
		args[i].Value = value
		if v, ok := value.(*LongData); ok {
			if _, err = c.longDataParameter(v); err != nil {
				return err
			}
		}
	}
	if c.session.mode == ModeOracle {
		plan, err := buildBindPlan(query, c.session.mode)
		if err != nil {
			return err
		}
		if _, err = bindArguments(plan, args); err != nil {
			return err
		}
	}
	return nil
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
	if input, _ := ctx.Value(localInfileContextKey{}).(*LocalInfile); input != nil {
		if c.session.capabilities&wire.ClientLocalFiles == 0 {
			c.unlock()
			return nil, ErrUnsupported
		}
		// MySQL LOAD DATA implicitly commits even DML preceding the import.
		if c.session.mode == ModeMySQL && (c.activeTx != nil || c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0 || c.xaUsed) {
			c.unlock()
			return nil, fmt.Errorf("%w: MySQL LOCAL INFILE requires autocommit without an active transaction", ErrUnsupported)
		}
	}
	if err := c.clearStatementsLocked(ctx); err != nil {
		c.unlock()
		return nil, err
	}
	return c.queryCommandLocked(ctx, append([]byte{3}, encoded...), false, nil)
}

func (c *conn) encodeSQL(query string) ([]byte, error) {
	return c.encodeSQLLimit(query, c.cfg.Limits.MaxMessageBytes-1)
}

func (c *conn) encodeSQLLimit(query string, limit int64) ([]byte, error) {
	if int64(utf8.RuneCountInString(query)) > limit {
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
	if int64(len(data)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	return data, nil
}

func (c *conn) startRows(ctx context.Context, binary bool, auto *stmt) *textRows {
	c.responseActive = true
	r := &textRows{conn: c, ctx: ctx, iterationCtx: ctx, done: true, binary: binary, autoStmt: auto}
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
	return c.queryCommandStateLocked(ctx, payload, binary, auto, keepGate, nil, nil)
}

func (c *conn) queryCommandStateLocked(ctx context.Context, payload []byte, binary bool, auto *stmt, keepGate bool, outputs *outputState, parameterStmt *stmt) (*textRows, error) {
	if !keepGate {
		c.clearDescriptors()
	}
	r := c.startRows(ctx, binary, auto)
	r.parameterStmt = parameterStmt
	r.keepGate = keepGate
	if !keepGate && !binary && len(payload) > 0 && payload[0] == 3 {
		r.localInfile, _ = ctx.Value(localInfileContextKey{}).(*LocalInfile)
	}
	if err := r.captureLOBOpen(); err != nil {
		return nil, r.fail(err, false)
	}
	r.outputs = outputs
	ctx = r.ctx
	if !keepGate {
		r.execution, _ = ctx.Value(executionContextKey{}).(*Execution)
	}
	c.session.packets.ResetSequence()
	if !keepGate {
		r.cancelExecution = c.beginCancelExecution()
	}
	if err := c.session.packets.WriteMessage(ctx, payload); err != nil {
		// A rejected pre-send packet does not poison the physical session.
		broken := !errorsPreSend(err)
		if broken {
			r.beginExecution()
		}
		return nil, r.fail(err, broken)
	}
	r.beginExecution()
	if err := r.advance(ctx); err != nil {
		return nil, r.fail(err, true)
	}
	if r.err != nil {
		return nil, r.err
	}
	if err := r.bufferLOBResponse(r.ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *textRows) captureLOBOpen() error {
	if state, _ := r.ctx.Value(lobOpenContextKey{}).(*lobOpenState); state != nil {
		r.captureLOB = true
		if !r.keepGate {
			state.gate, r.keepGate = true, true
		}
		if state.key.connection != nil && state.key != r.conn.resources.key {
			return ErrResource
		}
	}
	return nil
}

func errorsPreSend(err error) bool {
	return err == wire.ErrMessageTooLarge || err == context.Canceled || err == context.DeadlineExceeded
}

var (
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
)
