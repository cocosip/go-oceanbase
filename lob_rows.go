package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type lobRowset struct {
	columns []wire.Column
	rows    [][]driver.Value
	rawRows [][]byte
}

// decodeComplexRowsetLocked runs only after every response packet has been read.
func (r *textRows) decodeComplexRowsetLocked(ctx context.Context, set *lobRowset) error {
	if len(set.rawRows) == 0 {
		return nil
	}
	if err := r.conn.describeComplexColumnsLocked(ctx, set.columns); err != nil {
		return err
	}
	decoder := &textRows{conn: r.conn, columns: set.columns, binary: true, captureLOB: true, ctx: ctx, keepGate: true}
	for _, raw := range set.rawRows {
		if _, err := wire.DecodeBinaryRow(raw, set.columns); err != nil {
			r.conn.clearDescriptors()
			if err = r.conn.describeComplexColumnsLocked(ctx, set.columns); err != nil {
				return err
			}
			if _, err = wire.DecodeBinaryRow(raw, set.columns); err != nil {
				return err
			}
		}
		row := make([]driver.Value, len(set.columns))
		if err := decoder.decodeRow(raw, row); err != nil {
			return err
		}
		if err := r.conn.materializeComplexValuesLocked(ctx, set.columns, row); err != nil {
			return err
		}
		r.bufferBytes += lobValuesSize(row) - int64(len(raw))
		if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return wire.ErrMessageTooLarge
		}
		set.rows = append(set.rows, row)
	}
	set.rawRows = nil
	return nil
}

func (c *conn) materializeComplexValuesLocked(ctx context.Context, columns []wire.Column, values []driver.Value) error {
	for i, column := range columns {
		if column.Type != 160 || values[i] == nil {
			continue
		}
		raw, ok := values[i].([]byte)
		if !ok {
			return ErrComplex
		}
		value, err := c.complexValueLocked(ctx, column, raw)
		if err != nil {
			return err
		}
		values[i] = value
		if lobValuesSize(values) > c.cfg.Limits.MaxMessageBytes {
			return wire.ErrMessageTooLarge
		}
	}
	return nil
}

func (r *textRows) bufferLOBResponse(ctx context.Context) error {
	if r.captureLOB || r.released || r.conn.session.mode != ModeOracle {
		return nil
	}
	found := false
	for _, column := range r.columns {
		found = found || column.Type == 210 || column.Type == 211 || column.Type == 160 || column.Type == 163
	}
	if !found {
		return nil
	}
	r.holdRelease, r.captureLOB = true, true
	sets, err := r.collectLOBResponse(ctx)
	if err == nil {
		for i := range sets {
			set := &sets[i]
			if err = r.decodeComplexRowsetLocked(ctx, set); err != nil {
				break
			}
			for _, row := range set.rows {
				before := lobValuesSize(row)
				if err = r.conn.materializeLOBValuesLocked(ctx, set.columns, row, r.conn.cfg.Limits.MaxMessageBytes-r.bufferBytes+before); err != nil {
					break
				}
				r.bufferBytes += lobValuesSize(row) - before
				if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
					err = wire.ErrMessageTooLarge
					break
				}
			}
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		for i := range sets {
			if err = r.conn.materializeRefCursorsLocked(ctx, r.iterationCtx, &sets[i], 0, &r.bufferBytes, make(map[refCursorID]bool)); err != nil {
				break
			}
		}
	}
	r.holdRelease = false
	if err != nil {
		return r.fail(err, true)
	}
	r.releaseWithContext(ctx)
	if r.err != nil {
		return r.err
	}
	r.buffered, r.bufferRow, r.columns = sets, 0, sets[0].columns
	return nil
}

func (r *textRows) collectLOBResponse(ctx context.Context) ([]lobRowset, error) {
	var sets []lobRowset
	for {
		set := lobRowset{columns: r.columns}
		hasComplex := false
		for _, column := range r.columns {
			hasComplex = hasComplex || column.Type == 160
		}
		if hasComplex && !r.binary {
			return nil, fmt.Errorf("%w: complex rows require server prepared statements", ErrUnsupported)
		}
		r.bufferBytes += rowsetMetadataSize(r.columns)
		if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
		for !r.done {
			var row []driver.Value
			var err error
			if hasComplex {
				var data []byte
				data, err = r.rowPacket(ctx)
				if err == nil {
					set.rawRows = append(set.rawRows, data)
				}
			} else {
				row = make([]driver.Value, len(r.columns))
				err = r.next(ctx, row)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			r.bufferBytes += int64(len(r.columns))*16 + 24
			if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
				return nil, wire.ErrMessageTooLarge
			}
			if !hasComplex {
				set.rows = append(set.rows, row)
			}
		}
		sets = append(sets, set)
		if !r.more {
			break
		}
		if err := r.advance(ctx); err != nil {
			return nil, err
		}
		if r.done && !r.more {
			break
		}
	}
	return sets, nil
}

func lobValuesSize(values []driver.Value) int64 {
	var n int64
	for _, value := range values {
		switch v := value.(type) {
		case []byte:
			n += int64(len(v))
		case string:
			n += int64(len(v))
		}
	}
	return n
}

func (c *conn) materializeLOBValuesLocked(ctx context.Context, columns []wire.Column, values []driver.Value, limit int64) error {
	for i, column := range columns {
		if column.Type != 210 && column.Type != 211 || values[i] == nil {
			continue
		}
		raw, ok := values[i].([]byte)
		if !ok {
			return ErrLOB
		}
		locator, err := wire.DecodeLOBLocator(raw)
		if err != nil {
			return err
		}
		lob := &nativeLOBResource{raw: raw, locator: locator, column: column}
		value, err := c.materializeAndFreeLOBLocked(ctx, lob, limit-lobValuesSize(values)+int64(len(raw)))
		if err != nil {
			return err
		}
		values[i] = value
		if lobValuesSize(values) > limit {
			return wire.ErrMessageTooLarge
		}
	}
	return nil
}

func (c *conn) materializeAndFreeLOBLocked(ctx context.Context, lob *nativeLOBResource, limit int64) (_ driver.Value, err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, c.freeLOBLocked(cleanup, lob))
	}()
	if limit < 0 || lob.locator.ByteLength > uint64(c.cfg.Limits.MaxLOBBytes) || lob.locator.ByteLength > uint64(math.MaxInt) || lob.locator.ByteLength > uint64(limit) {
		return nil, wire.ErrMessageTooLarge
	}
	return c.materializeLOBLocked(ctx, lob, min(limit, c.cfg.Limits.MaxLOBBytes))
}

func (c *conn) materializeLOBLocked(ctx context.Context, lob *nativeLOBResource, limit int64) (driver.Value, error) {
	if lob.column.Type == 211 {
		text, err := c.readLOBCharsLocked(ctx, lob, 0, int64(lob.locator.ByteLength), limit)
		if err == io.EOF {
			err = nil
		}
		return text, err
	}
	data := make([]byte, int(lob.locator.ByteLength))
	if lob.locator.InRow {
		copy(data, lob.locator.Data)
		return data, nil
	}
	_, err := c.readLOBBytesLocked(ctx, lob, data, 0)
	return data, err
}

func rowsetMetadataSize(columns []wire.Column) int64 {
	n := int64(32)
	for _, col := range columns {
		n += 128 + int64(len(col.Catalog)+len(col.Schema)+len(col.Table)+len(col.OriginalTable)+len(col.Name)+len(col.OriginalName)+len(col.Extra)+len(col.ComplexSchema)+len(col.ComplexName)+len(col.ElementSchema)+len(col.ElementName))
	}
	return n
}
