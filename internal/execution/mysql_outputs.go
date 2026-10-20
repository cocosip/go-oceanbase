package execution

import (
	"context"
	"errors"

	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (e *Engine) mysqlLongOutputs(ctx context.Context, op *session.Operation, query []byte, outputs *call.State, options Options) (_ *Rows, err error) {
	owned := true
	var prepared *statement.Statement
	defer func() {
		if owned {
			cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
			defer cancel()
			if prepared != nil {
				err = errors.Join(err, prepared.Close(cleanup, op))
			}
			err = errors.Join(err, outputs.Finish(cleanup, op, false))
			if !options.RetainOperation {
				e.release(op)
			}
		}
	}()
	text, err := charset.Decode(op.Config().Session.Charset, query)
	if err != nil {
		return nil, err
	}
	prepared, err = statement.Prepare(ctx, op, text)
	if err != nil {
		return nil, err
	}
	values, err := parameters.Bind(prepared.Plan(), outputs.LongArguments())
	if err != nil {
		return nil, err
	}
	budget := op.Config().Limits.MaxMessageBytes - 10
	if len(values) > 0 {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	params, err := parameters.Encode(ctx, op, &e.catalog, values, budget)
	if err != nil {
		return nil, err
	}
	payload, err := wire.EncodeStmtExecute(prepared.ID(), params, op.Config().Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	if err = statement.UploadLongData(ctx, op, prepared.ID(), values); err != nil {
		return nil, err
	}
	owned = false
	return e.start(ctx, op, payload, true, prepared, nil, outputs, options, false, false)
}
