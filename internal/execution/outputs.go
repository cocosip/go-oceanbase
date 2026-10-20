package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// queryOutputs owns op, including cleanup if validation fails before rows can own it.
func (e *Engine) queryOutputs(ctx context.Context, op *session.Operation, query string, args []driver.NamedValue, prepared *statement.Statement, options Options) (_ *Rows, err error) {
	e.catalog.Clear()
	owned, temporary := true, false
	defer func() {
		if owned {
			if temporary {
				cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
				_ = prepared.Close(cleanup, op)
				cancel()
			}
			if !options.RetainOperation {
				e.release(op)
			}
		}
	}()
	if prepared != nil {
		if err = prepared.CheckOpen(op); err != nil {
			return nil, err
		}
	}
	if err = e.statements.Clear(ctx, op); err != nil {
		return nil, err
	}
	if int64(utf8.RuneCountInString(query)) >= op.Config().Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	plan, err := sqltext.BuildBindPlanEscapes(query, op.Protocol().Mode, op.Protocol().OK.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	values, err := e.bind(ctx, op, plan, args)
	if err != nil {
		return nil, err
	}
	outputs, err := call.NewState(op, plan, values)
	if err != nil {
		return nil, err
	}
	if outputs.HasResources() && !options.AllowResources {
		return nil, dberrors.ErrOut
	}
	if op.Protocol().Mode == config.ModeOracle {
		for _, value := range values {
			if source, ok := value.(*streaminput.Source); ok {
				if _, err = statement.LongDataParameter(op, source); err != nil {
					return nil, err
				}
			}
		}
		if hasLong(values) && prepared == nil {
			prepared, err = statement.Prepare(ctx, op, plan.Query)
			if err != nil {
				return nil, errors.Join(err, outputs.CheckEpoch(op))
			}
			temporary = true
		}
		if err = outputs.CheckEpoch(op); err != nil {
			return nil, err
		}
		payload, err := call.PreparePayload(ctx, op, &e.catalog, plan, values, prepared)
		if err = errors.Join(err, outputs.CheckEpoch(op)); err != nil {
			return nil, err
		}
		if hasLong(values) {
			err = statement.UploadLongData(ctx, op, prepared.ID(), values)
			if err = errors.Join(err, outputs.CheckEpoch(op)); err != nil {
				return nil, err
			}
		}
		owned = false
		return e.combined(ctx, op, payload, plan, outputs, prepared, options, temporary)
	}
	initial, queryBytes, err := outputs.MySQLSQL(op, plan, values)
	if err != nil {
		return nil, err
	}
	if err = e.outputCommand(ctx, op, initial); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
		defer cancel()
		return nil, errors.Join(err, outputs.Finish(cleanup, op, false))
	}
	owned = false
	if len(outputs.LongArguments()) > 0 {
		return e.mysqlLongOutputs(ctx, op, queryBytes, outputs, options)
	}
	return e.start(ctx, op, append([]byte{3}, queryBytes...), false, nil, nil, outputs, options, false, false)
}

func (e *Engine) outputCommand(ctx context.Context, op *session.Operation, query []byte) error {
	r, err := e.start(ctx, op, append([]byte{3}, query...), false, nil, nil, nil, Options{}, true, false)
	if err != nil {
		return err
	}
	return r.Drain(ctx)
}
