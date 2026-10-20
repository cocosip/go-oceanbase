package catalog

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ErrResponseActive preserves the internal-command response boundary error.
var ErrResponseActive = errors.New("oceanbase: an unread response prevents an internal command")

// Result is a fully buffered ordinary dictionary rowset.
type Result struct {
	Columns []wire.Column
	Rows    [][]driver.Value
	Bytes   int64
	More    bool
}

// QueryRows reads a bounded dictionary response through the held operation.
// It never acquires or releases the operation and cannot prepare a user query.
func QueryRows(ctx context.Context, op *session.Operation, plan Query) (Result, error) {
	sets, err := QuerySets(ctx, op, plan)
	if err != nil {
		return Result{}, err
	}
	if len(sets) != 1 {
		return Result{}, dberrors.ErrComplex
	}
	return sets[0], nil
}

// QuerySets buffers ordinary dictionary results while preserving complete response boundaries.
func QuerySets(ctx context.Context, op *session.Operation, plan Query) ([]Result, error) {
	if op.ResponseActive() {
		return nil, ErrResponseActive
	}
	if op.Closed() {
		return nil, driver.ErrBadConn
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query, err := command.Interpolate(op, plan.SQL, plan.Args)
	if err != nil {
		return nil, err
	}
	r, err := command.Start(ctx, op, append([]byte{3}, query...), false)
	if err != nil {
		return nil, err
	}
	r.BoundBytes(true)
	var results []Result
	var overhead, charged int64
	for {
		event, err := r.Advance(ctx)
		if err != nil {
			return nil, err
		}
		if event.Kind == command.InfileRequest {
			op.Discard()
			op.EndResponse()
			return nil, dberrors.ErrUnsupported
		}
		if len(results) == 0 && len(plan.Columns) > 0 && len(event.Columns) != len(plan.Columns) {
			return nil, errors.Join(fmt.Errorf("oceanbase: metadata projection count differs from plan"), r.Drain(ctx))
		}
		set := Result{Columns: event.Columns}
		overhead += valuecodec.SnapshotMetadataSize(set.Columns)
		if overhead > op.Config().Limits.MaxMessageBytes {
			op.Discard()
			op.EndResponse()
			return nil, wire.ErrMessageTooLarge
		}
		for !r.Done() {
			packet, err := r.NextPacket(ctx)
			if err != nil && err != io.EOF {
				return nil, err
			}
			if overhead > op.Config().Limits.MaxMessageBytes-r.BufferBytes() {
				op.Discard()
				op.EndResponse()
				return nil, wire.ErrMessageTooLarge
			}
			if err == io.EOF {
				break
			}
			fields, err := wire.DecodeTextRow(packet, len(set.Columns))
			if err != nil {
				op.Discard()
				op.EndResponse()
				return nil, err
			}
			row := make([]driver.Value, len(fields))
			cfg, protocol := op.Config(), op.Protocol()
			for i, field := range fields {
				if typ := set.Columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > cfg.Limits.MaxLOBBytes {
					op.Discard()
					op.EndResponse()
					return nil, wire.ErrMessageTooLarge
				}
				row[i], err = valuecodec.DecodeRowValue(set.Columns[i], false, field, protocol.Mode, cfg.Session)
				if err != nil {
					op.Discard()
					op.EndResponse()
					return nil, err
				}
			}
			overhead += int64(len(set.Columns))*16 + 24
			if overhead > cfg.Limits.MaxMessageBytes-r.BufferBytes() {
				op.Discard()
				op.EndResponse()
				return nil, wire.ErrMessageTooLarge
			}
			set.Rows = append(set.Rows, row)
		}
		set.More = r.More()
		set.Bytes = overhead + r.BufferBytes() - charged
		charged += set.Bytes
		results = append(results, set)
		if !r.More() {
			break
		}
	}
	return results, nil
}
