package execution

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Prepare creates a server statement under the session gate.
func (e *Engine) Prepare(ctx context.Context, query string) (*statement.Statement, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer e.release(op)
	return statement.Prepare(ctx, op, query)
}

// CloseStatement bounds statement cleanup and tolerates a discarded connection.
func (e *Engine) CloseStatement(s *statement.Statement) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.Config().Limits.CleanupTimeout)
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		if errors.Is(err, driver.ErrBadConn) && e.physical.Closed() {
			return nil
		}
		e.Discard()
		return err
	}
	defer e.release(op)
	return s.Close(ctx, op)
}

// QueryPrepared executes an explicit server statement.
func (e *Engine) QueryPrepared(ctx context.Context, s *statement.Statement, args []driver.NamedValue, options Options) (*Rows, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if options.Outputs || hasOutputs(args) {
		return e.queryOutputs(ctx, op, s.Plan().Query, args, s, options)
	}
	return e.queryPrepared(ctx, op, s, args, options)
}

// queryPrepared owns op on entry and transfers it only after all encoding succeeds.
func (e *Engine) queryPrepared(ctx context.Context, op *session.Operation, s *statement.Statement, args []driver.NamedValue, options Options) (_ *Rows, err error) {
	owned := true
	defer func() {
		if owned {
			if s.Automatic() {
				cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
				_ = s.Close(cleanup, op)
				cancel()
			}
			if !options.RetainOperation {
				e.release(op)
			}
		}
	}()
	e.catalog.Clear()
	if err = s.CheckOpen(op); err != nil {
		return nil, err
	}
	plan := s.Plan()
	values, err := e.bind(ctx, op, plan, args)
	if err != nil {
		return nil, err
	}
	if !statement.CacheableSQL(op, plan.Query) {
		if err = e.statements.Clear(ctx, op); err != nil {
			return nil, err
		}
	}
	if statement.UsePrepareExecute(op) && !hasLong(values) {
		payload, err := call.PreparePayload(ctx, op, &e.catalog, plan, values, s)
		if err != nil {
			return nil, err
		}
		owned = false
		return e.combined(ctx, op, payload, plan, nil, s, options, false)
	}
	budget := op.Config().Limits.MaxMessageBytes - 10
	if len(values) > 0 {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	params, err := parameters.Encode(ctx, op, &e.catalog, values, budget)
	if err != nil {
		return nil, err
	}
	query, err := command.EncodeSQL(plan.SQL, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, err
	}
	payload, err := wire.EncodeStmtExecuteOptions(s.ID(), params, op.Config().Limits.MaxMessageBytes, s.ParameterOptions(op, query, params, false))
	if err != nil {
		return nil, err
	}
	if err = statement.UploadLongData(ctx, op, s.ID(), values); err != nil {
		return nil, err
	}
	var auto *statement.Statement
	if s.Automatic() {
		auto = s
	}
	owned = false
	return e.start(ctx, op, payload, true, auto, s, nil, options, false, false)
}
