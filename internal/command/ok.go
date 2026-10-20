package command

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ExecOK executes a command that must return one terminal OK packet.
func ExecOK(ctx context.Context, op *session.Operation, payload []byte) (wire.OK, error) {
	if _, err := Start(ctx, op, payload, false); err != nil {
		return wire.OK{}, err
	}
	return ReadOK(ctx, op)
}

// ReadOK consumes one terminal OK or server error response.
func ReadOK(ctx context.Context, op *session.Operation) (wire.OK, error) {
	defer op.EndResponse()
	data, err := op.Packets().ReadMessage(ctx)
	if err != nil {
		op.Discard()
		return wire.OK{}, err
	}
	if len(data) > 0 && data[0] == 255 {
		err = DecodeServerError(data)
		if err == wire.ErrMalformed {
			op.Discard()
		}
		return wire.OK{}, err
	}
	ok, err := wire.DecodeOK(data, op.Protocol().Capabilities)
	if err != nil {
		op.Discard()
		return wire.OK{}, err
	}
	if ok.Status&8 != 0 {
		op.Discard()
		return wire.OK{}, fmt.Errorf("%w: unexpected additional command results", dberrors.ErrUnsupported)
	}
	op.SetOK(ok)
	return ok, nil
}

// InitSQL executes initialization SQL and drains its complete response.
func InitSQL(ctx context.Context, op *session.Operation, encodedSQL []byte) error {
	r, err := Start(ctx, op, append([]byte{3}, encodedSQL...), false)
	if err != nil {
		return err
	}
	event, err := r.Advance(ctx)
	if err != nil {
		return err
	}
	if event.Kind == InfileRequest {
		return r.fail(dberrors.ErrUnsupported)
	}
	return r.Drain(ctx)
}
