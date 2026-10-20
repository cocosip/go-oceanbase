package materialize

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// CollectSet only receives bytes; descriptor and LOB queries wait for the full response.
func (c *Decoder) CollectSet(ctx context.Context, r *command.RawResponse, binaryRows bool, used *int64) (Set, error) {
	r.BoundBytes(true)
	set := Set{Columns: r.Columns()}
	hasComplex := false
	for _, column := range set.Columns {
		hasComplex = hasComplex || column.Type == 160
	}
	if hasComplex && !binaryRows {
		return Set{}, fmt.Errorf("%w: complex rows require server prepared statements", dberrors.ErrUnsupported)
	}
	*used += valuecodec.SnapshotMetadataSize(set.Columns)
	if *used > c.op.Config().Limits.MaxMessageBytes {
		return Set{}, wire.ErrMessageTooLarge
	}
	for !r.Done() {
		before := r.BufferBytes()
		data, err := r.NextPacket(ctx)
		*used += r.BufferBytes() - before
		if *used > c.op.Config().Limits.MaxMessageBytes {
			return Set{}, wire.ErrMessageTooLarge
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Set{}, err
		}
		*used += 24 + 16*int64(len(set.Columns))
		if *used > c.op.Config().Limits.MaxMessageBytes {
			return Set{}, wire.ErrMessageTooLarge
		}
		if hasComplex {
			set.RawRows = append(set.RawRows, data)
			continue
		}
		var fields [][]byte
		if binaryRows {
			fields, err = wire.DecodeBinaryRow(data, set.Columns)
		} else {
			fields, err = wire.DecodeTextRow(data, len(set.Columns))
		}
		if err != nil {
			return Set{}, err
		}
		row, err := Row(c.op, set.Columns, binaryRows, fields, true)
		if err != nil {
			return Set{}, err
		}
		set.Rows = append(set.Rows, row)
	}
	return set, nil
}

// CollectResponse begins at the current rowset and consumes all remaining boundaries.
func (c *Decoder) CollectResponse(ctx context.Context, r *command.RawResponse, binaryRows bool, used *int64) ([]Set, error) {
	var sets []Set
	for {
		if !r.Done() || r.Columns() != nil {
			set, err := c.CollectSet(ctx, r, binaryRows, used)
			if err != nil {
				return nil, err
			}
			sets = append(sets, set)
		}
		if !r.More() {
			return sets, nil
		}
		event, err := r.Advance(ctx)
		if err != nil {
			return nil, err
		}
		if event.Kind == command.InfileRequest {
			return nil, dberrors.ErrUnsupported
		}
	}
}

// DecodeSet replaces only buffered bytes and never replays the business statement.
func (c *Decoder) DecodeSet(ctx context.Context, set *Set, used *int64) error {
	if c.op.ResponseActive() {
		return catalog.ErrResponseActive
	}
	if len(set.RawRows) > 0 {
		if err := errors.Join(c.resolver.DescribeColumns(ctx, c.op, set.Columns), c.CheckEpoch()); err != nil {
			return err
		}
		for _, raw := range set.RawRows {
			fields, err := wire.DecodeBinaryRow(raw, set.Columns)
			if err != nil {
				c.resolver.Clear()
				if err = errors.Join(c.resolver.DescribeColumns(ctx, c.op, set.Columns), c.CheckEpoch()); err != nil {
					return err
				}
				fields, err = wire.DecodeBinaryRow(raw, set.Columns)
				if err != nil {
					return err
				}
			}
			row, err := Row(c.op, set.Columns, true, fields, true)
			if err != nil {
				return err
			}
			if err = errors.Join(c.ComplexValues(ctx, set.Columns, row), c.CheckEpoch()); err != nil {
				return err
			}
			*used += valuecodec.SnapshotValuesSize(row) - int64(len(raw))
			if *used > c.op.Config().Limits.MaxMessageBytes {
				return wire.ErrMessageTooLarge
			}
			set.Rows = append(set.Rows, row)
		}
		set.RawRows = nil
	}
	for _, row := range set.Rows {
		before := valuecodec.SnapshotValuesSize(row)
		err := c.LOBValues(ctx, set.Columns, row, c.op.Config().Limits.MaxMessageBytes-*used+before)
		if err = errors.Join(err, c.CheckEpoch()); err != nil {
			return err
		}
		*used += valuecodec.SnapshotValuesSize(row) - before
		if *used > c.op.Config().Limits.MaxMessageBytes {
			return wire.ErrMessageTooLarge
		}
	}
	return c.CheckEpoch()
}

// Fetch receives and materializes a single native cursor batch, preserving its final status.
func (c *Decoder) Fetch(ctx context.Context, id uint32, count int, columns []wire.Column, options command.FetchOptions) (Set, uint16, command.Result, error) {
	r, err := command.FetchRaw(ctx, c.op, id, count, columns, options)
	if err != nil {
		var server *dberrors.Error
		if errors.As(err, &server) {
			c.op.Discard()
		}
		return Set{}, 0, command.Result{}, err
	}
	var used int64
	sets, err := c.CollectResponse(ctx, r, true, &used)
	if err != nil {
		c.op.Discard()
		c.op.EndResponse()
		return Set{}, 0, command.Result{}, err
	}
	if err = c.CheckEpoch(); err != nil {
		return Set{}, 0, command.Result{}, err
	}
	if len(sets) != 1 || len(sets[0].Rows)+len(sets[0].RawRows) > count || columns != nil && len(sets[0].Columns) != len(columns) {
		c.op.Discard()
		c.op.EndResponse()
		return Set{}, 0, command.Result{}, dberrors.ErrCursor
	}
	err = c.DecodeSet(ctx, &sets[0], &used)
	if err != nil {
		c.op.Discard()
		c.op.EndResponse()
	}
	return sets[0], r.Status(), r.Last(), err
}
