package execution

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
)

// Query dispatches normalized arguments and transfers the operation to its response.
func (e *Engine) Query(ctx context.Context, query string, args []driver.NamedValue, options Options) (*Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	return e.queryLocked(ctx, op, query, args, options)
}

// queryLocked transfers op to rows, unless RetainOperation keeps it with the caller on every exit.
func (e *Engine) queryLocked(ctx context.Context, op *session.Operation, query string, args []driver.NamedValue, options Options) (*Rows, error) {
	var err error
	epoch := op.Epoch()
	owned := true
	defer func() {
		if owned && !options.RetainOperation {
			e.release(op)
		}
	}()
	if options.Outputs || hasOutputs(args) {
		owned = false
		return e.queryOutputs(ctx, op, query, args, nil, options)
	}
	if len(args) == 0 {
		payload, err := command.EncodeSQL(query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
		if err != nil {
			return nil, err
		}
		if err = e.validateImport(op, query, options.Input); err != nil {
			return nil, err
		}
		if op.Config().Execution.StatementCacheSize > 0 && !statement.CacheableSQL(op, query) && !statement.SessionNeutralSQL(op, query) {
			if err = e.statements.Clear(ctx, op); err != nil {
				return nil, err
			}
		}
		owned = false
		return e.start(ctx, op, append([]byte{3}, payload...), false, nil, nil, nil, options, false, false)
	}
	if op.Config().PrepareMode == config.PrepareClient {
		plan, err := sqltext.BuildBindPlanEscapes(query, op.Protocol().Mode, op.Protocol().OK.Status&512 != 0)
		if err != nil {
			return nil, err
		}
		values, err := e.bind(ctx, op, plan, args)
		if err != nil {
			return nil, err
		}
		payload, err := statement.Interpolate(op, plan, values)
		if err != nil {
			return nil, err
		}
		owned = false
		return e.start(ctx, op, append([]byte{3}, payload...), false, nil, nil, nil, options, false, false)
	}
	args, err = parameters.PrepareArguments(op, query, args)
	if err != nil {
		return nil, err
	}
	if hasOutputs(args) {
		owned = false
		return e.queryOutputs(ctx, op, query, args, nil, options)
	}
	combined := statement.UsePrepareExecute(op)
	for _, arg := range args {
		combined = combined && !hasLong([]any{arg.Value})
	}
	prepared, err := e.statements.Take(ctx, op, query)
	if err == nil && prepared == nil {
		prepared, err = statement.PrepareAutomatic(ctx, op, query, combined)
	}
	if err != nil {
		return nil, err
	}
	if options.CaptureLOB && op.Epoch() != epoch {
		cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
		defer cancel()
		return nil, errors.Join(dberrors.ErrResource, prepared.Close(cleanup, op))
	}
	owned = false
	return e.queryPrepared(ctx, op, prepared, args, options)
}

// Exec consumes all results before returning the final command count.
func (e *Engine) Exec(ctx context.Context, query string, args []driver.NamedValue, options Options) (command.Result, error) {
	rows, err := e.Query(ctx, query, args, options)
	if err != nil {
		return command.Result{}, err
	}
	if err = rows.Drain(ctx); err != nil {
		return command.Result{}, err
	}
	return rows.Last(), nil
}

func (e *Engine) start(ctx context.Context, op *session.Operation, payload []byte, binaryRows bool, auto, parameter *statement.Statement, outputs *call.State, options Options, retain, capture bool) (*Rows, error) {
	if !retain {
		e.catalog.Clear()
	}
	r := e.newRows(ctx, op, binaryRows, options)
	r.auto, r.parameter, r.outputs = auto, parameter, outputs
	r.retainOperation = r.retainOperation || retain
	r.captureLOB = r.captureLOB || capture
	if !r.retainOperation {
		r.cancelExecution = e.physical.BeginCancellation()
	}
	raw, err := command.Start(r.ctx, op, payload, binaryRows)
	r.raw = raw
	if err != nil {
		broken := !command.PreSend(err)
		if broken {
			r.begin()
		}
		return nil, r.fail(err, broken)
	}
	r.begin()
	if err = r.advance(r.ctx); err != nil {
		return nil, r.fail(err, true)
	}
	if r.err != nil {
		return nil, r.err
	}
	if err = r.bufferResources(r.ctx); err != nil {
		return nil, err
	}
	return r, nil
}
