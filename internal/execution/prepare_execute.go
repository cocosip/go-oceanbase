package execution

import (
	"context"

	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (e *Engine) combined(ctx context.Context, op *session.Operation, payload []byte, plan sqltext.Plan, outputs *call.State, prepared *statement.Statement, options Options, temporary bool) (*Rows, error) {
	r := e.newRows(ctx, op, true, options)
	r.outputs, r.parameter = outputs, prepared
	if temporary || prepared != nil && prepared.Automatic() {
		r.auto = prepared
	}
	if !r.retainOperation {
		r.cancelExecution = e.physical.BeginCancellation()
	}
	raw, err := command.Start(r.ctx, op, payload, true)
	r.raw = raw
	if err != nil {
		broken := !command.PreSend(err)
		if broken {
			r.begin()
		}
		return nil, r.fail(err, broken)
	}
	r.begin()
	data, err := op.Packets().ReadMessage(r.ctx)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if len(data) > 0 && data[0] == 255 {
		err = command.DecodeServerError(data)
		if err != wire.ErrMalformed {
			r.recorder.Add(ResultInfo{Kind: ResultFailure, Complete: true, Err: err})
		}
		return nil, r.fail(err, err == wire.ErrMalformed)
	}
	header, params, budget, err := command.CombinedMetadata(r.ctx, op, data)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if err = r.acceptCombined(plan, prepared, header, params); err != nil {
		return nil, r.fail(err, true)
	}
	raw.NativeTail(true)
	if !header.HasRows {
		op.Packets().RestartResponseSequence()
	} else {
		if err = raw.BeginRowset(r.ctx, int(header.Columns), budget); err != nil {
			return nil, r.fail(err, true)
		}
		r.columns = raw.Columns()
		hidden := header.Flags&5 != 0 || outputs != nil && op.Protocol().Capabilities&wire.ClientDeprecateEOF == 0 && op.Protocol().OK.Status&4096 != 0
		if !hidden {
			if err = r.recorder.Rowset(r.columns, op.Config().Limits.MaxMessageBytes); err != nil {
				return nil, r.fail(err, true)
			}
			if err = r.bufferResources(r.ctx); err != nil {
				return nil, err
			}
			return r, nil
		}
		if outputs == nil {
			return nil, r.fail(wire.ErrMalformed, true)
		}
		if err = outputs.ReadHidden(r.ctx, op, raw); err != nil {
			return nil, r.fail(err, true)
		}
	}
	if err = r.advance(r.ctx); err != nil {
		return nil, r.fail(err, true)
	}
	if err = r.bufferResources(r.ctx); err != nil {
		return nil, err
	}
	return r, r.err
}

func (r *Rows) acceptCombined(plan sqltext.Plan, prepared *statement.Statement, header wire.PrepareExecuteOK, params []wire.Column) error {
	if prepared == nil {
		r.auto = statement.Adopt(r.op, header.ID, plan, params)
	} else if err := prepared.AcceptCombined(r.op, header, params); err != nil {
		return err
	}
	if r.outputs != nil {
		parent := prepared
		if parent == nil {
			parent = r.auto
		}
		r.outputs.Parent(parent)
	}
	if int(header.Parameters) != len(plan.Slots) || r.outputs == nil && header.Flags != 0 {
		return wire.ErrMalformed
	}
	return nil
}
