package execution

import (
	"context"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/batch"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Batch preserves raw qualification separately from deferred logical arguments.
func (e *Engine) Batch(ctx context.Context, query string, rawRows, rows [][]any, options batch.Options) (*batch.Result, error) {
	result := new(batch.Result)
	plan, err := batch.NewPlan(rows, options, result)
	if err != nil {
		return result, err
	}
	options, err = batch.NormalizeOptions(options)
	if err != nil {
		return result, err
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return result, err
	}
	watch := e.newRows(ctx, op, false, Options{RetainOperation: true})
	outcome := new(batch.NativeOutcome)
	handled, err := batch.TryNative(watch.ctx, op, &e.catalog, &e.statements, &lifetime.Owners{Cursors: &e.cursors, LOBs: &e.lobs}, batch.Request{Query: query, RawRows: rawRows, Rows: rows, Options: options, Result: result, Outcome: outcome}, e.writable(op, true))
	if outcome.WarningsChanged {
		e.control.BeginSQL()
		if outcome.WarningsKnown {
			e.control.ObserveOK(wire.OK{Warnings: outcome.WarningCount})
		}
	}
	watch.err = err
	watch.finish(watch.ctx)
	e.release(op)
	if handled {
		return result, watch.err
	}
	prepareErr := outcome.PrepareErr
	for {
		index, values, ok := plan.Next(ctx)
		if !ok {
			break
		}
		observation := batch.Observation{Native: true}
		if prepareErr != nil {
			observation.Err = prepareErr
			prepareErr = nil
		} else {
			args, err := parameters.NamedArguments(values)
			if err != nil {
				observation.Err = err
			} else {
				attempt := new(Attempt)
				var res driver.Result
				res, err = e.Exec(ctx, query, args, Options{Attempt: attempt})
				if attempt.Acknowledged {
					res = attempt.Result
				}
				observation.Result, observation.Err = res, err
				observation.Started, observation.Acknowledged = attempt.Started, attempt.Acknowledged
			}
		}
		observation.Broken = e.physical.Closed()
		plan.Observe(index, observation)
	}
	return result, plan.Err()
}
