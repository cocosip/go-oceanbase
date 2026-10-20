package command

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// OpenCursor sends a cursor command and validates its metadata response.
func OpenCursor(ctx context.Context, op *session.Operation, payload []byte) ([]wire.Column, bool, error) {
	r, err := Start(ctx, op, payload, true)
	if err != nil {
		return nil, false, err
	}
	defer op.EndResponse()
	event, err := r.Advance(ctx)
	if err != nil {
		return nil, false, err
	}
	if event.Kind != RowResult {
		return nil, false, r.fail(wire.ErrMalformed)
	}
	p := op.Protocol()
	if p.Capabilities&wire.ClientDeprecateEOF == 0 && p.OK.Status&(64|128) == 0 {
		return nil, false, r.fail(fmt.Errorf("%w: server did not open a cursor", dberrors.ErrUnsupported))
	}
	return event.Columns, p.Capabilities&wire.ClientDeprecateEOF == 0 && p.OK.Status&128 != 0, nil
}

// FetchOptions selects the wire orientation and cursor offset.
type FetchOptions struct {
	Offset    bool
	Sensitive bool
	Start     int64
}

// FetchRaw returns a live raw response. Materialization and cursor position belong to callers.
func FetchRaw(ctx context.Context, op *session.Operation, id uint32, count int, columns []wire.Column, options FetchOptions) (*RawResponse, error) {
	payload := binary.LittleEndian.AppendUint32([]byte{28}, id)
	payload = binary.LittleEndian.AppendUint32(payload, uint32(count))
	if options.Offset {
		orientation, offset := uint16(2), uint32(0)
		if options.Sensitive {
			orientation, offset = 32, uint32(options.Start)
		}
		payload = binary.LittleEndian.AppendUint16(payload, orientation)
		payload = binary.LittleEndian.AppendUint32(payload, offset)
		payload = binary.LittleEndian.AppendUint32(payload, 1)
	}
	r, err := Start(ctx, op, payload, true)
	if err != nil {
		return nil, err
	}
	r.boundBytes = true
	if op.Protocol().Mode == config.ModeOracle {
		if _, err = r.Advance(ctx); err != nil {
			return nil, err
		}
		r.nativeTail = options.Offset
	} else {
		r.columns, r.done = columns, false
	}
	return r, nil
}
