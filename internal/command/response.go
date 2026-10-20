package command

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// MaxColumns bounds server metadata and generated result shapes.
const MaxColumns = 4096

// EventKind identifies a raw protocol result boundary.
type EventKind uint8

// Raw response event kinds.
const (
	CommandResult EventKind = iota + 1
	RowResult
	InfileRequest
)

// Event exposes one result boundary, leaving hidden outputs and recording to the caller.
type Event struct {
	Kind    EventKind
	OK      wire.OK
	Columns []wire.Column
	Request []byte
}

// RawResponse owns only raw response state. It never releases the operation.
type RawResponse struct {
	op                                *session.Operation
	columns                           []wire.Column
	binary                            bool
	done, more, nativeTail, infileAck bool
	items                             int
	last                              Result
	status                            uint16
	bufferBytes                       int64
	boundBytes                        bool
}

// Start sends one command while retaining the caller's operation.
func Start(ctx context.Context, op *session.Operation, payload []byte, binary bool) (*RawResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := op.BeginResponse(); err != nil {
		return nil, err
	}
	p := op.Packets()
	p.ResetSequence()
	if err := p.WriteMessage(ctx, payload); err != nil {
		op.EndResponse()
		if !PreSend(err) {
			op.Discard()
		}
		return nil, err
	}
	return &RawResponse{op: op, binary: binary, done: true}, nil
}

// Adopt consumes a response whose command has already been sent by a specialized primitive.
func Adopt(op *session.Operation, binary bool) *RawResponse {
	return &RawResponse{op: op, binary: binary, done: true}
}

// Columns returns the current raw column descriptions.
func (r *RawResponse) Columns() []wire.Column { return r.columns }

// Done reports that the current rowset has ended.
func (r *RawResponse) Done() bool { return r.done }

// More reports an additional result boundary.
func (r *RawResponse) More() bool { return r.more }

// Items returns the number of result headers consumed.
func (r *RawResponse) Items() int { return r.items }

// Last returns the most recent command result.
func (r *RawResponse) Last() Result { return r.last }

// Status returns the last observed server status.
func (r *RawResponse) Status() uint16 { return r.status }

// NativeTail marks whether an OceanBase native final status is expected.
func (r *RawResponse) NativeTail(value bool) { r.nativeTail = value }

// BoundBytes enables aggregate raw result accounting.
func (r *RawResponse) BoundBytes(value bool) { r.boundBytes = value }

// BufferBytes returns the raw bytes charged to this response.
func (r *RawResponse) BufferBytes() int64 { return r.bufferBytes }

func (r *RawResponse) fail(err error) error {
	var server *dberrors.Error
	if !errors.As(err, &server) {
		r.op.Discard()
	}
	r.op.EndResponse()
	return err
}

// Advance reads exactly one command, rowset, or authorized-upload request header.
func (r *RawResponse) Advance(ctx context.Context) (Event, error) {
	data, err := r.op.Packets().ReadMessage(ctx)
	if err != nil {
		return Event{}, r.fail(err)
	}
	if len(data) == 0 {
		return Event{}, r.fail(wire.ErrMalformed)
	}
	if r.infileAck {
		r.infileAck = false
		if data[0] != 0 && data[0] != 255 {
			return Event{}, r.fail(wire.ErrMalformed)
		}
	}
	if data[0] == 251 {
		r.infileAck = true
		return Event{Kind: InfileRequest, Request: data}, nil
	}
	if data[0] == 255 {
		return Event{}, r.fail(DecodeServerError(data))
	}
	if r.nativeTail {
		if data[0] != 0 {
			return Event{}, r.fail(wire.ErrMalformed)
		}
		r.nativeTail = false
	}
	r.items++
	if r.items > r.op.Config().Limits.MaxResultItems {
		return Event{}, r.fail(fmt.Errorf("oceanbase: result item limit exceeded"))
	}
	if data[0] == 0 {
		ok, err := wire.DecodeOK(data, r.op.Protocol().Capabilities)
		if err != nil {
			return Event{}, r.fail(err)
		}
		r.last = Result{Affected: ok.AffectedRows, ID: ok.InsertID, Known: true}
		r.op.SetOK(ok)
		r.more, r.done, r.columns = ok.Status&8 != 0, true, nil
		if !r.more {
			r.op.EndResponse()
		}
		return Event{Kind: CommandResult, OK: ok}, nil
	}
	n, err := wire.DecodeColumnCount(data, MaxColumns)
	if err != nil {
		return Event{}, r.fail(err)
	}
	budget := int64(len(data))
	columns, err := ReadMetadata(ctx, r.op, n, &budget, true)
	if err != nil {
		return Event{}, r.fail(err)
	}
	r.columns, r.done, r.more = columns, false, false
	return Event{Kind: RowResult, Columns: columns}, nil
}

// NextFields returns unmaterialized protocol fields. EOF records a complete rowset boundary.
func (r *RawResponse) NextFields(ctx context.Context) ([][]byte, error) {
	data, err := r.NextPacket(ctx)
	if err != nil {
		return nil, err
	}
	var fields [][]byte
	if r.binary {
		fields, err = wire.DecodeBinaryRow(data, r.columns)
	} else {
		fields, err = wire.DecodeTextRow(data, len(r.columns))
	}
	if err != nil {
		return nil, r.fail(err)
	}
	return fields, nil
}

// NextPacket preserves the original row bytes for deferred complex decoding.
func (r *RawResponse) NextPacket(ctx context.Context) ([]byte, error) {
	if r.done {
		return nil, io.EOF
	}
	data, err := r.op.Packets().ReadMessage(ctx)
	if err != nil {
		return nil, r.fail(err)
	}
	if r.boundBytes {
		if int64(len(data)) > r.op.Config().Limits.MaxMessageBytes-r.bufferBytes {
			return nil, r.fail(wire.ErrMessageTooLarge)
		}
		r.bufferBytes += int64(len(data))
	}
	if len(data) == 0 {
		return nil, r.fail(wire.ErrMalformed)
	}
	if data[0] == 255 {
		return nil, r.fail(DecodeServerError(data))
	}
	caps := r.op.Protocol().Capabilities
	if data[0] == 254 && (len(data) < 9 || caps&wire.ClientDeprecateEOF != 0 && len(data) < 0xffffff) {
		var ok wire.OK
		if caps&wire.ClientDeprecateEOF != 0 {
			ok, err = wire.DecodeOK(data, caps)
		} else {
			ok, err = wire.DecodeEOF(data)
		}
		if err != nil {
			return nil, r.fail(err)
		}
		r.op.SetOK(ok)
		r.done, r.status, r.more = true, ok.Status, ok.Status&8 != 0 || r.nativeTail
		if !r.more {
			r.op.EndResponse()
		}
		return nil, io.EOF
	}
	return data, nil
}

// NextValues decodes ordinary scalar fields; resource-aware callers use NextFields.
func (r *RawResponse) NextValues(ctx context.Context) ([]driver.Value, error) {
	fields, err := r.NextFields(ctx)
	if err != nil {
		return nil, err
	}
	cfg, p := r.op.Config(), r.op.Protocol()
	values := make([]driver.Value, len(fields))
	for i, field := range fields {
		if typ := r.columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > cfg.Limits.MaxLOBBytes {
			return nil, r.fail(wire.ErrMessageTooLarge)
		}
		values[i], err = valuecodec.DecodeRowValue(r.columns[i], r.binary, field, p.Mode, cfg.Session)
		if err != nil {
			return nil, r.fail(err)
		}
	}
	return values, nil
}

// Drain consumes all remaining rows and result boundaries.
func (r *RawResponse) Drain(ctx context.Context) error {
	for {
		for !r.done {
			if _, err := r.NextFields(ctx); err != nil && err != io.EOF {
				return err
			}
		}
		if !r.more {
			return nil
		}
		event, err := r.Advance(ctx)
		if err != nil {
			return err
		}
		if event.Kind == InfileRequest {
			return r.fail(dberrors.ErrUnsupported)
		}
	}
}
