package statement

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// NativeResult preserves native-call progress and raw hidden output rows.
// Values is populated only when every output column is scalar; complex rows remain deferred.
type NativeResult struct {
	Started, Seen bool
	Columns       []wire.Column
	Rows          [][]byte
	Values        []driver.Value
	ResultItems   int
}

// NativeCallRaw executes an internal native call without entering business OUT handling.
// The caller retains its operation. Temporary statement cleanup uses a separate timeout.
func NativeCallRaw(ctx context.Context, op *session.Operation, query string, params []wire.Parameter, count int) (result *NativeResult, err error) {
	result = &NativeResult{}
	if op.Closed() {
		return result, dberrors.ErrResource
	}
	if op.ResponseActive() || !SupportsPrepareExecute(op.Protocol().OBVersion) {
		return result, dberrors.ErrUnsupported
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if count < 0 {
		return result, dberrors.ErrOut
	}
	payload, err := wire.EncodePrepareExecute(0, []byte(query), params, op.Config().Limits.MaxMessageBytes)
	if err != nil {
		return result, err
	}
	raw, err := command.Start(ctx, op, payload, true)
	if err != nil {
		result.Started = !command.PreSend(err)
		return result, err
	}
	result.Started = true
	var temporary *Statement
	var use *Use
	defer func() {
		result.ResultItems = raw.Items()
		if op.ResponseActive() {
			op.Discard()
			op.EndResponse()
		}
		if temporary == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, temporary.Close(cleanup, op), use.Release(cleanup, op))
	}()
	data, err := op.Packets().ReadMessage(ctx)
	if err != nil {
		op.Discard()
		return result, err
	}
	if len(data) > 0 && data[0] == 255 {
		err = command.DecodeServerError(data)
		if errors.Is(err, wire.ErrMalformed) {
			op.Discard()
		}
		op.EndResponse()
		return result, err
	}
	header, parameters, budget, err := command.CombinedMetadata(ctx, op, data)
	if err != nil {
		op.Discard()
		return result, err
	}
	if int(header.Parameters) != len(params) {
		op.Discard()
		return result, wire.ErrMalformed
	}
	temporary = Adopt(op, header.ID, sqltext.Plan{}, parameters)
	use, err = temporary.AcquireLease(op)
	if err != nil {
		op.Discard()
		return result, err
	}
	raw.NativeTail(true)
	output := nativeOutput{op: op, raw: raw, result: result, count: count}
	if header.HasRows {
		if err = raw.BeginRowset(ctx, int(header.Columns), budget); err != nil {
			return result, err
		}
		hidden := header.Flags&5 != 0 || hiddenMetadata(op)
		if err = output.consume(ctx, hidden); err != nil {
			return result, err
		}
	} else {
		op.Packets().RestartResponseSequence()
	}
	for {
		event, e := raw.Advance(ctx)
		if e != nil {
			return result, e
		}
		switch event.Kind {
		case command.RowResult:
			if err = output.consume(ctx, hiddenMetadata(op)); err != nil {
				return result, err
			}
		case command.InfileRequest:
			op.Discard()
			return result, dberrors.ErrUnsupported
		}
		if !raw.More() {
			break
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if output.err != nil {
		return result, output.err
	}
	if !result.Seen {
		return result, fmt.Errorf("%w: output response missing", dberrors.ErrOut)
	}
	return result, nil
}

type nativeOutput struct {
	op       *session.Operation
	raw      *command.RawResponse
	result   *NativeResult
	count    int
	buffered int64
	err      error
}

func (o *nativeOutput) consume(ctx context.Context, hidden bool) error {
	columns := o.raw.Columns()
	hasComplex := false
	for _, column := range columns {
		if column.Type == 160 {
			hasComplex = true
		}
	}
	if hidden {
		if o.result.Columns != nil || len(columns) != o.count {
			o.err = nativeShapeError()
		}
		o.result.Columns = slices.Clone(columns)
	}
	for {
		row, err := o.raw.NextPacket(ctx)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var values []driver.Value
		if !hasComplex {
			values, err = decodeNativeScalars(o.op, columns, row)
			if err != nil {
				o.op.Discard()
				return err
			}
		}
		if !hidden {
			continue
		}
		if int64(len(row))+24 > o.op.Config().Limits.MaxMessageBytes-o.buffered {
			o.op.Discard()
			return wire.ErrMessageTooLarge
		}
		o.buffered += int64(len(row)) + 24
		o.result.Rows = append(o.result.Rows, slices.Clone(row))
		if o.result.Seen || len(columns) != o.count {
			o.err = nativeShapeError()
			continue
		}
		o.result.Seen = true
		o.result.Values = values
	}
}

func nativeShapeError() error {
	return fmt.Errorf("%w: output row cardinality or column count", dberrors.ErrOut)
}

func hiddenMetadata(op *session.Operation) bool {
	p := op.Protocol()
	return p.Capabilities&wire.ClientDeprecateEOF == 0 && p.OK.Status&4096 != 0
}

func decodeNativeScalars(op *session.Operation, columns []wire.Column, row []byte) ([]driver.Value, error) {
	fields, err := wire.DecodeBinaryRow(row, columns)
	if err != nil {
		return nil, err
	}
	values := make([]driver.Value, len(fields))
	cfg, p := op.Config(), op.Protocol()
	for i, field := range fields {
		if typ := columns[i].Type; typ == 210 || typ == 211 {
			if field != nil {
				values[i] = slices.Clone(field)
			}
			continue
		}
		if typ := columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > cfg.Limits.MaxLOBBytes {
			return nil, wire.ErrMessageTooLarge
		}
		values[i], err = valuecodec.DecodeRowValue(columns[i], true, field, p.Mode, cfg.Session)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}
