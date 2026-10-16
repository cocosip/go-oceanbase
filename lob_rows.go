package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type lobRowset struct {
	columns []wire.Column
	rows    [][]driver.Value
}

func (r *textRows) bufferLOBResponse() error {
	if r.captureLOB || r.released || r.conn.session.mode != ModeOracle {
		return nil
	}
	found := false
	for _, column := range r.columns {
		found = found || column.Type == 210 || column.Type == 211
	}
	if !found {
		return nil
	}
	r.holdRelease, r.captureLOB = true, true
	sets, err := r.collectLOBResponse()
	if err == nil {
		for _, set := range sets {
			for _, row := range set.rows {
				before := lobValuesSize(row)
				if err = r.conn.materializeLOBValuesLocked(r.ctx, set.columns, row, r.conn.cfg.Limits.MaxMessageBytes-r.bufferBytes+before); err != nil {
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
	r.holdRelease = false
	if err != nil {
		return r.fail(err, true)
	}
	r.release()
	if r.err != nil {
		return r.err
	}
	r.buffered, r.bufferRow, r.columns = sets, 0, sets[0].columns
	return nil
}

func (r *textRows) collectLOBResponse() ([]lobRowset, error) {
	var sets []lobRowset
	for {
		set := lobRowset{columns: r.columns}
		r.bufferBytes += 32
		for _, column := range r.columns {
			r.bufferBytes += 128 + int64(len(column.Catalog)) + int64(len(column.Schema)) + int64(len(column.Table)) + int64(len(column.OriginalTable)) + int64(len(column.Name)) + int64(len(column.OriginalName))
		}
		if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
		for !r.done {
			row := make([]driver.Value, len(r.columns))
			err := r.next(r.ctx, row)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			r.bufferBytes += int64(len(row))*16 + 24
			if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
				return nil, wire.ErrMessageTooLarge
			}
			set.rows = append(set.rows, row)
		}
		sets = append(sets, set)
		if !r.more {
			break
		}
		if err := r.advance(r.ctx); err != nil {
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
