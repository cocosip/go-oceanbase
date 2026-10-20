// Package materialize resolves resource values only after their enclosing response is complete.
package materialize

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// RefCursorID preserves a nested cursor identity until resource adoption.
type RefCursorID uint32

// Set buffers one raw result for deferred descriptor and LOB resolution.
type Set struct {
	Columns []wire.Column
	Rows    [][]driver.Value
	RawRows [][]byte
}

// Decoder binds materialization to one session epoch.
type Decoder struct {
	op       *session.Operation
	resolver *catalog.Resolver
	epoch    session.Epoch
}

// New creates a decoder scoped to the current operation epoch.
func New(op *session.Operation, resolver *catalog.Resolver) *Decoder {
	return &Decoder{op: op, resolver: resolver, epoch: op.Epoch()}
}

// CheckEpoch rejects materialization after connection or transaction changes.
func (c *Decoder) CheckEpoch() error {
	if c.op.Closed() || c.op.Epoch() != c.epoch {
		return dberrors.ErrResource
	}
	return nil
}

// Row decodes scalar cells while preserving locators, complex bytes and child cursor IDs.
func Row(op *session.Operation, columns []wire.Column, binaryRows bool, fields [][]byte, captureLOB bool) ([]driver.Value, error) {
	if len(fields) != len(columns) {
		return nil, wire.ErrMalformed
	}
	values := make([]driver.Value, len(fields))
	for i, field := range fields {
		switch columns[i].Type {
		case 163:
			if op.Protocol().Mode != config.ModeOracle || !binaryRows {
				return nil, dberrors.ErrUnsupported
			}
			if field != nil {
				if len(field) != 4 {
					return nil, wire.ErrMalformed
				}
				id := binary.LittleEndian.Uint32(field)
				if id == 0 {
					return nil, dberrors.ErrCursor
				}
				values[i] = RefCursorID(id)
			}
			continue
		case 160:
			if !binaryRows {
				return nil, fmt.Errorf("%w: complex rows require server prepared statements", dberrors.ErrUnsupported)
			}
			if field != nil {
				values[i] = append([]byte{}, field...)
			}
			continue
		case 210, 211:
			if captureLOB && op.Protocol().Mode == config.ModeOracle {
				if field != nil {
					values[i] = slices.Clone(field)
				}
				continue
			}
		}
		if typ := columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > op.Config().Limits.MaxLOBBytes {
			return nil, wire.ErrMessageTooLarge
		}
		value, err := valuecodec.DecodeRowValue(columns[i], binaryRows, field, op.Protocol().Mode, op.Config().Session)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

// ComplexValue resolves one named Oracle value after response completion.
func (c *Decoder) ComplexValue(ctx context.Context, column wire.Column, data []byte) (driver.Value, error) {
	return c.complexValueLocked(ctx, column, data)
}

// ComplexValues resolves all complex columns within the result budget.
func (c *Decoder) ComplexValues(ctx context.Context, columns []wire.Column, values []driver.Value) error {
	for i, column := range columns {
		if column.Type != 160 || values[i] == nil {
			continue
		}
		raw, ok := values[i].([]byte)
		if !ok {
			return dberrors.ErrComplex
		}
		value, err := c.ComplexValue(ctx, column, raw)
		if err != nil {
			return err
		}
		values[i] = value
		if valuecodec.SnapshotValuesSize(values) > c.op.Config().Limits.MaxMessageBytes {
			return wire.ErrMessageTooLarge
		}
	}
	return nil
}

// LOBValues materializes locator columns within the supplied limit.
func (c *Decoder) LOBValues(ctx context.Context, columns []wire.Column, values []driver.Value, limit int64) error {
	return c.materializeLOBValuesLocked(ctx, columns, values, limit)
}

// ReadLOB materializes and frees one native locator.
func (c *Decoder) ReadLOB(ctx context.Context, n *lob.Native, limit int64) (driver.Value, error) {
	return c.materializeAndFreeLOBLocked(ctx, n, limit)
}
