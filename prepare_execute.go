package oceanbase

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

var prepareExecuteVersion = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)

func supportsPrepareExecute(version string) bool { return supportsVersion(version, 2, 2, 76) }

func supportsVersion(version string, major, minor, patch uint32) bool {
	parts := prepareExecuteVersion.FindStringSubmatch(version)
	if parts == nil {
		return false
	}
	var numbers [3]uint32
	for i := range numbers {
		n, err := strconv.ParseUint(parts[i+1], 10, 32)
		if err != nil {
			return false
		}
		numbers[i] = uint32(n)
	}
	return slices.Compare(numbers[:], []uint32{major, minor, patch}) >= 0
}

func (c *conn) outputPrepareExecute(ctx context.Context, plan bindPlan, values []boundValue, prepared *stmt) ([]byte, error) {
	if !supportsPrepareExecute(c.session.obVersion) {
		return nil, fmt.Errorf("%w: Oracle output bindings require OceanBase >= 2.2.76", ErrUnsupported)
	}
	query, err := c.encodeSQL(plan.sql)
	if err != nil {
		return nil, err
	}
	budget := c.cfg.Limits.MaxMessageBytes - 30 - (wire.Parameter{Data: query, LengthEncoded: true}).EncodedSize()
	if len(values) > 0 {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	if budget < 0 {
		return nil, wire.ErrMessageTooLarge
	}
	params := make([]wire.Parameter, len(values))
	for i, v := range values {
		if b, ok := v.value.(*outBinding); ok {
			v.value = b.input
			if b.In {
				if err = c.validateOutputValue(b, b.input); err != nil {
					return nil, err
				}
			}
		}
		limit := budget
		switch v.value.(type) {
		case Raw, []byte:
			limit = min(limit, c.cfg.Limits.MaxLOBBytes)
		}
		params[i], err = c.encodeParameterLocked(ctx, v.value, limit)
		if err != nil {
			return nil, err
		}
		if (params[i].EncodedSize() + int64(len(params[i].TypeMetadata))) > budget {
			return nil, wire.ErrMessageTooLarge
		}
		budget -= (params[i].EncodedSize() + int64(len(params[i].TypeMetadata)))
	}
	var id uint32
	options := c.executeOptions(query, true)
	if prepared != nil {
		id = prepared.id
		options = prepared.parameterOptions(query, params, true)
	}
	return wire.EncodePrepareExecuteOptions(id, query, params, c.cfg.Limits.MaxMessageBytes, options)
}

func (c *conn) queryPrepareExecuteLocked(ctx context.Context, payload []byte, plan bindPlan, outputs *outputState, prepared *stmt) (*textRows, error) {
	r := c.startRows(ctx, true, nil)
	r.parameterStmt = prepared
	if prepared != nil && prepared.automatic || outputs != nil && outputs.temporary {
		r.autoStmt = prepared
	}
	r.outputs = outputs
	if outputs != nil && outputs.internal {
		r.keepGate, r.captureLOB = true, true
	} else {
		r.execution, _ = ctx.Value(executionContextKey{}).(*Execution)
	}
	if err := r.captureLOBOpen(); err != nil {
		return nil, r.fail(err, false)
	}
	ctx = r.ctx
	p := c.session.packets
	p.ResetSequence()
	if !r.keepGate {
		r.cancelExecution = c.beginCancelExecution()
	}
	if err := p.WriteMessage(ctx, payload); err != nil {
		broken := !errorsPreSend(err)
		if outputs != nil {
			outputs.started = broken
		}
		if broken {
			r.beginExecution()
		}
		return nil, r.fail(err, broken)
	}
	if outputs != nil {
		outputs.started = true
	}
	r.beginExecution()
	data, err := p.ReadMessage(ctx)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if len(data) > 0 && data[0] == 255 {
		err = decodeServerError(data)
		if err != wire.ErrMalformed {
			r.execution.add(ResultInfo{Kind: ResultFailure, Complete: true, Err: err})
		}
		return nil, r.fail(err, err == wire.ErrMalformed)
	}
	header, budget, err := r.prepareExecuteMetadata(ctx, data, plan, prepared)
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.nativeTail = true
	if !header.HasRows {
		p.RestartResponseSequence()
	}
	if header.HasRows {
		r.items++
		r.hiddenOut = header.Flags&5 != 0
		if err = r.readColumns(ctx, int(header.Columns), budget); err != nil {
			return nil, r.fail(err, true)
		}
		if !r.hiddenOut {
			if err = r.bufferLOBResponse(r.ctx); err != nil {
				return nil, err
			}
			return r, nil
		}
		if err = r.readOutputs(ctx); err != nil {
			return nil, err
		}
	}
	if err = r.advance(ctx); err != nil {
		return nil, r.fail(err, true)
	}
	if err = r.bufferLOBResponse(r.ctx); err != nil {
		return nil, err
	}
	return r, r.err
}

func (r *textRows) prepareExecuteMetadata(ctx context.Context, data []byte, plan bindPlan, prepared *stmt) (wire.PrepareExecuteOK, int64, error) {
	header, err := wire.DecodePrepareExecuteOK(data)
	if err != nil {
		return header, 0, err
	}
	if prepared == nil {
		r.autoStmt = &stmt{conn: r.conn, id: header.ID, epoch: r.conn.statementEpoch}
	} else if prepared.id == 0 {
		prepared.id = header.ID
	} else if header.ID != prepared.id {
		return header, 0, wire.ErrMalformed
	}
	if r.outputs != nil {
		r.outputs.parent = prepared
		if r.outputs.parent == nil {
			r.outputs.parent = r.autoStmt
		}
	}
	if header.ID == 0 || int(header.Parameters) != len(plan.slots) || header.Columns > maxTextColumns || header.Flags&^uint32(7) != 0 || header.Flags == 2 || header.Flags == 6 || r.outputs == nil && header.Flags != 0 {
		return header, 0, wire.ErrMalformed
	}
	budget := int64(len(data))
	parameters, err := r.conn.prepareMetadata(ctx, int(header.Parameters), &budget)
	if err != nil {
		return header, 0, err
	}
	if prepared != nil {
		prepared.parameters = parameters
	}
	return header, budget, nil
}
