package execution

import (
	"context"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
)

// PrepareClient validates a statement without sending PREPARE.
func (e *Engine) PrepareClient(ctx context.Context, query string) (*statement.Client, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer e.release(op)
	return statement.PrepareClient(op, query)
}

// QueryClient binds and interpolates a client statement under the session gate.
func (e *Engine) QueryClient(ctx context.Context, s *statement.Client, args []driver.NamedValue, options Options) (*Rows, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			e.release(op)
		}
	}()
	if err = s.CheckOpen(op); err != nil {
		return nil, err
	}
	if options.Outputs || hasOutputs(args) {
		owned = false
		return e.queryOutputs(ctx, op, s.Query(), args, nil, options)
	}
	plan, err := sqltext.BuildBindPlanEscapes(s.Query(), op.Protocol().Mode, op.Protocol().OK.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	values, err := e.bind(ctx, op, plan, args)
	if err != nil {
		return nil, err
	}
	query, err := statement.Interpolate(op, plan, values)
	if err != nil {
		return nil, err
	}
	owned = false
	return e.start(ctx, op, append([]byte{3}, query...), false, nil, nil, nil, options, false, false)
}
