package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"math"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) tableBlobValueLocked(ctx context.Context, lob *tableBlobResource, projection string, args []driver.NamedValue, lock bool) (driver.Value, error) {
	query := "SELECT " + projection + lob.from()
	if lock {
		query += " FOR UPDATE"
	}
	values := append(args, lob.args...)
	for i := range values {
		values[i].Ordinal = i + 1
	}
	sets, err := c.tableBlobQueryLocked(ctx, query, values, false)
	if err != nil {
		return nil, err
	}
	return tableBlobScalar(sets)
}

func (c *conn) execTableBlobLocked(ctx context.Context, lob *tableBlobResource, r lobRequest) (driver.Result, error) {
	var err error
	switch r.operation {
	case lobSize:
		value, e := c.tableBlobValueLocked(ctx, lob, "OCTET_LENGTH("+lob.column+")", nil, false)
		if e != nil {
			return nil, e
		}
		r.result.size, err = tableBlobLength(value)
	case lobRead:
		err = c.readTableBlobLocked(ctx, lob, r)
	case lobWrite, lobTruncate:
		err = c.mutateTableBlobLocked(ctx, lob, r)
	default:
		return nil, ErrUnsupported
	}
	if err == nil {
		_, err = c.resourceLocked(r.handle)
	}
	return result{known: true}, err
}

func (c *conn) readTableBlobLocked(ctx context.Context, lob *tableBlobResource, r lobRequest) error {
	chunk := min(int64(32767), c.cfg.Limits.MaxMessageBytes/4, c.cfg.Limits.MaxLOBBytes)
	if chunk < 1 {
		return wire.ErrMessageTooLarge
	}
	for r.result.n < len(r.data) {
		if _, err := c.resourceLocked(r.handle); err != nil {
			return err
		}
		offset := r.offset + int64(r.result.n)
		if offset == math.MaxInt64 {
			return ErrLOB
		}
		amount := min(int64(len(r.data)-r.result.n), chunk)
		args := namedValues([]driver.Value{offset + 1, amount})
		value, err := c.tableBlobValueLocked(ctx, lob, "SUBSTRING("+lob.column+",?,?)", args, false)
		if err != nil {
			return err
		}
		data, ok := value.([]byte)
		if !ok || int64(len(data)) > amount {
			return ErrLOB
		}
		r.result.n += copy(r.data[r.result.n:], data)
		if int64(len(data)) < amount {
			return io.EOF
		}
	}
	return nil
}

func (c *conn) mutateTableBlobLocked(ctx context.Context, lob *tableBlobResource, r lobRequest) error {
	if _, ok := r.handle.session.(*sql.Tx); !ok || c.activeTx == nil || c.activeTx.readOnly || c.session.ok.Status&8192 != 0 || !lob.locking {
		return ErrUnsupported
	}
	value, err := c.tableBlobValueLocked(ctx, lob, "OCTET_LENGTH("+lob.column+")", nil, true)
	if err != nil {
		return err
	}
	length, err := tableBlobLength(value)
	if err != nil {
		return err
	}
	if _, err = c.resourceLocked(r.handle); err != nil {
		return err
	}
	if r.operation == lobTruncate {
		if r.count > length {
			return ErrLOB
		}
		if r.count == length {
			return nil
		}
		return c.updateTableBlobLocked(ctx, lob, "LEFT("+lob.column+",?)", namedValues([]driver.Value{r.count}))
	}
	if r.offset > length {
		return ErrLOB
	}
	if len(r.data) == 0 {
		return nil
	}
	if max(length, r.offset+r.count) > lob.capacity {
		return fmt.Errorf("%w: table BLOB capacity exceeded", ErrLOB)
	}
	if r.offset+r.count == math.MaxInt64 {
		return ErrLOB
	}
	query := "CONCAT(LEFT(" + lob.column + ",?),?,SUBSTRING(" + lob.column + ",?))"
	// Binary client literals use two SQL bytes per content byte; reserve all fixed key/SQL overhead first.
	baseArgs := append(namedValues([]driver.Value{r.offset, []byte{}, r.offset + r.count + 1}), lob.args...)
	base, err := c.interpolate("UPDATE "+lob.table+" SET "+lob.column+" = "+query+" WHERE "+lob.predicate, baseArgs)
	if err != nil {
		return err
	}
	chunk := min(int64(32767), c.cfg.Limits.MaxLOBBytes, (c.cfg.Limits.MaxMessageBytes-1-int64(len(base)))/2)
	if chunk < 1 && len(r.data) != 0 {
		return wire.ErrMessageTooLarge
	}
	for r.result.n < len(r.data) {
		if _, err = c.resourceLocked(r.handle); err != nil {
			return err
		}
		n := min(len(r.data)-r.result.n, int(chunk))
		off := r.offset + int64(r.result.n)
		err = c.updateTableBlobLocked(ctx, lob, query, namedValues([]driver.Value{off, r.data[r.result.n : r.result.n+n], off + int64(n) + 1}))
		if err != nil {
			return err
		}
		r.result.n += n
	}
	return nil
}

func (c *conn) updateTableBlobLocked(ctx context.Context, lob *tableBlobResource, expression string, args []driver.NamedValue) error {
	args = append(args, lob.args...)
	query, err := c.interpolate("UPDATE "+lob.table+" SET "+lob.column+" = "+expression+" WHERE "+lob.predicate, args)
	if err != nil {
		return err
	}
	r, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, query...), false, nil, true)
	if err != nil {
		return err
	}
	if err = r.drainAll(ctx); err != nil {
		return err
	}
	if c.session.ok.Warnings != 0 {
		return fmt.Errorf("%w: table BLOB mutation returned warnings; roll back the caller transaction", ErrLOB)
	}
	if !r.last.known || r.last.affected > 1 {
		return ErrLOB
	}
	return nil
}
