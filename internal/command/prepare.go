package command

import (
	"context"
	"encoding/binary"

	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Prepare allocates a server statement and returns its parameter and result metadata.
func Prepare(ctx context.Context, op *session.Operation, query []byte) (wire.PrepareOK, []wire.Column, []wire.Column, error) {
	if _, err := Start(ctx, op, append([]byte{22}, query...), false); err != nil {
		return wire.PrepareOK{}, nil, nil, err
	}
	defer op.EndResponse()
	data, err := op.Packets().ReadMessage(ctx)
	if err != nil {
		op.Discard()
		return wire.PrepareOK{}, nil, nil, err
	}
	if len(data) > 0 && data[0] == 255 {
		err = DecodeServerError(data)
		if err == wire.ErrMalformed {
			op.Discard()
		}
		return wire.PrepareOK{}, nil, nil, err
	}
	header, err := wire.DecodePrepareOK(data)
	if err == nil && header.Columns > MaxColumns {
		err = wire.ErrMessageTooLarge
	}
	if err != nil {
		op.Discard()
		return header, nil, nil, err
	}
	budget := int64(len(data))
	parameters, err := ReadMetadata(ctx, op, int(header.Parameters), &budget, false)
	var columns []wire.Column
	if err == nil {
		columns, err = ReadMetadata(ctx, op, int(header.Columns), &budget, false)
	}
	if err != nil {
		op.Discard()
	}
	return header, parameters, columns, err
}

// CloseStatement retires a server statement without awaiting a response.
func CloseStatement(ctx context.Context, op *session.Operation, id uint32) error {
	if op.Closed() || id == 0 {
		return nil
	}
	if err := op.BeginResponse(); err != nil {
		return err
	}
	defer op.EndResponse()
	p := op.Packets()
	p.ResetSequence()
	err := p.WriteMessage(ctx, binary.LittleEndian.AppendUint32([]byte{25}, id))
	if err != nil && !PreSend(err) {
		op.Discard()
	}
	return err
}

// CombinedMetadata decodes only the raw header and parameter descriptions.
func CombinedMetadata(ctx context.Context, op *session.Operation, data []byte) (wire.PrepareExecuteOK, []wire.Column, int64, error) {
	header, err := wire.DecodePrepareExecuteOK(data)
	if err != nil {
		return header, nil, 0, err
	}
	if header.ID == 0 || header.Columns > MaxColumns || header.Flags&^uint32(7) != 0 || header.Flags == 2 || header.Flags == 6 {
		return header, nil, 0, wire.ErrMalformed
	}
	budget := int64(len(data))
	parameters, err := ReadMetadata(ctx, op, int(header.Parameters), &budget, false)
	return header, parameters, budget, err
}

// BeginRowset adopts native row metadata while preserving result budgets.
func (r *RawResponse) BeginRowset(ctx context.Context, count int, budget int64) error {
	r.items++
	if r.items > r.op.Config().Limits.MaxResultItems {
		return r.fail(wire.ErrMessageTooLarge)
	}
	if count < 1 || count > MaxColumns {
		return r.fail(wire.ErrMessageTooLarge)
	}
	columns, err := ReadMetadata(ctx, r.op, count, &budget, true)
	if err != nil {
		return r.fail(err)
	}
	r.columns, r.done, r.more = columns, false, false
	return nil
}
