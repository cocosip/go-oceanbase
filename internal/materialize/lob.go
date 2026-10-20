package materialize

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *Decoder) materializeLOBValuesLocked(ctx context.Context, columns []wire.Column, values []driver.Value, limit int64) error {
	for i, column := range columns {
		if column.Type != 210 && column.Type != 211 || values[i] == nil {
			continue
		}
		raw, ok := values[i].([]byte)
		if !ok {
			return dberrors.ErrLOB
		}
		n, err := lob.DecodeLocator(column, raw)
		if err != nil {
			return err
		}
		value, err := c.materializeAndFreeLOBLocked(ctx, n, limit-valuecodec.SnapshotValuesSize(values)+int64(len(raw)))
		if err != nil {
			return err
		}
		values[i] = value
		if valuecodec.SnapshotValuesSize(values) > limit {
			return wire.ErrMessageTooLarge
		}
	}
	return nil
}

func (c *Decoder) materializeAndFreeLOBLocked(ctx context.Context, n *lob.Native, limit int64) (_ driver.Value, err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.op.Config().Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, lob.Free(cleanup, c.op, n))
	}()
	length := n.ByteLength()
	if limit < 0 || length > c.op.Config().Limits.MaxLOBBytes || uint64(length) > uint64(math.MaxInt) || length > limit {
		return nil, wire.ErrMessageTooLarge
	}
	limit = min(limit, c.op.Config().Limits.MaxLOBBytes)
	if n.Column().Type == 211 {
		text, e := lob.ReadText(ctx, c.op, n, 0, length, limit)
		if e == io.EOF {
			e = nil
		}
		return text, e
	}
	data := make([]byte, int(length))
	if n.InRow() {
		copy(data, n.InRowData())
		return data, nil
	}
	_, err = lob.ExecuteNative(ctx, c.op, n, lob.Read, 0, length, data, "")
	return data, err
}
