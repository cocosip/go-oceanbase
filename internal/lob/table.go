package lob

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"math"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type tableOperation struct {
	op       *session.Operation
	ref      session.ResourceRef
	writable bool
}

func (c *tableOperation) validate() error { return c.op.Validate(c.ref) }

func namedValues(values []driver.Value) []driver.NamedValue {
	args := make([]driver.NamedValue, len(values))
	for i, v := range values {
		args[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return args
}

func (c *tableOperation) tableBlobValueLocked(ctx context.Context, lob *catalog.TableBlobPlan, projection string, args []driver.NamedValue, lock bool) (driver.Value, error) {
	query := "SELECT " + projection + lob.From()
	if lock {
		query += " FOR UPDATE"
	}
	values := append(args, lob.Args...)
	for i := range values {
		values[i].Ordinal = i + 1
	}
	epoch := c.op.Epoch()
	result, err := catalog.QueryRows(ctx, c.op, catalog.Query{SQL: query, Args: values})
	if err != nil {
		return nil, err
	}
	if epoch != c.op.Epoch() {
		return nil, dberrors.ErrResource
	}
	return catalog.Scalar(result)
}

func (c *tableOperation) execute(ctx context.Context, lob *catalog.TableBlobPlan, operation Operation, offset, count int64, data []byte, text string) (Result, error) {
	result := Result{}
	if offset < 0 || count < 0 || offset > math.MaxInt64-count {
		return result, dberrors.ErrLOB
	}
	request := Request{operation: operation, offset: offset, count: count, data: data, text: text, result: &result}
	var err error
	switch operation {
	case Size:
		var value driver.Value
		value, err = c.tableBlobValueLocked(ctx, lob, "OCTET_LENGTH("+lob.Column+")", nil, false)
		if err == nil {
			result.size, err = catalog.Length(value)
		}
	case Read:
		err = c.readTableBlobLocked(ctx, lob, request)
	case Write, Truncate:
		err = c.mutateTableBlobLocked(ctx, lob, request)
	default:
		err = dberrors.ErrUnsupported
	}
	if err == nil {
		err = c.op.Validate(c.ref)
	}
	return result, err
}

func (c *tableOperation) updateTableBlobLocked(ctx context.Context, lob *catalog.TableBlobPlan, expression string, args []driver.NamedValue) error {
	args = append(args, lob.Args...)
	query, err := command.Interpolate(c.op, "UPDATE "+lob.Table+" SET "+lob.Column+" = "+expression+" WHERE "+lob.Predicate, args)
	if err != nil {
		return err
	}
	epoch := c.op.Epoch()
	r, err := command.Start(ctx, c.op, append([]byte{3}, query...), false)
	if err != nil {
		return err
	}
	if _, err = r.Advance(ctx); err != nil {
		return err
	}
	if err = r.Drain(ctx); err != nil {
		return err
	}
	if epoch != c.op.Epoch() {
		return dberrors.ErrResource
	}
	if c.op.Protocol().OK.Warnings != 0 {
		return fmt.Errorf("%w: table BLOB mutation returned warnings; roll back the caller transaction", dberrors.ErrLOB)
	}
	if !r.Last().Known || r.Last().Affected > 1 {
		return dberrors.ErrLOB
	}
	return nil
}

func (c *tableOperation) readTableBlobLocked(ctx context.Context, lob *catalog.TableBlobPlan, r Request) error {
	chunk := min(int64(32767), c.op.Config().Limits.MaxMessageBytes/4, c.op.Config().Limits.MaxLOBBytes)
	if chunk < 1 {
		return wire.ErrMessageTooLarge
	}
	for r.result.n < len(r.data) {
		if err := c.validate(); err != nil {
			return err
		}
		offset := r.offset + int64(r.result.n)
		if offset == math.MaxInt64 {
			return dberrors.ErrLOB
		}
		amount := min(int64(len(r.data)-r.result.n), chunk)
		args := namedValues([]driver.Value{offset + 1, amount})
		value, err := c.tableBlobValueLocked(ctx, lob, "SUBSTRING("+lob.Column+",?,?)", args, false)
		if err != nil {
			return err
		}
		data, ok := value.([]byte)
		if !ok || int64(len(data)) > amount {
			return dberrors.ErrLOB
		}
		r.result.n += copy(r.data[r.result.n:], data)
		if int64(len(data)) < amount {
			return io.EOF
		}
	}
	return nil
}

func (c *tableOperation) mutateTableBlobLocked(ctx context.Context, lob *catalog.TableBlobPlan, r Request) error {
	if !c.writable || c.op.Protocol().OK.Status&8192 != 0 || !lob.Locking {
		return dberrors.ErrUnsupported
	}
	value, err := c.tableBlobValueLocked(ctx, lob, "OCTET_LENGTH("+lob.Column+")", nil, true)
	if err != nil {
		return err
	}
	length, err := catalog.Length(value)
	if err != nil {
		return err
	}
	if err = c.validate(); err != nil {
		return err
	}
	if r.operation == Truncate {
		if r.count > length {
			return dberrors.ErrLOB
		}
		if r.count == length {
			return nil
		}
		return c.updateTableBlobLocked(ctx, lob, "LEFT("+lob.Column+",?)", namedValues([]driver.Value{r.count}))
	}
	if r.offset > length {
		return dberrors.ErrLOB
	}
	if len(r.data) == 0 {
		return nil
	}
	if max(length, r.offset+r.count) > lob.Capacity {
		return fmt.Errorf("%w: table BLOB capacity exceeded", dberrors.ErrLOB)
	}
	if r.offset+r.count == math.MaxInt64 {
		return dberrors.ErrLOB
	}
	query := "CONCAT(LEFT(" + lob.Column + ",?),?,SUBSTRING(" + lob.Column + ",?))"
	// Binary client literals use two SQL bytes per content byte; reserve all fixed key/SQL overhead first.
	baseArgs := append(namedValues([]driver.Value{r.offset, []byte{}, r.offset + r.count + 1}), lob.Args...)
	base, err := command.Interpolate(c.op, "UPDATE "+lob.Table+" SET "+lob.Column+" = "+query+" WHERE "+lob.Predicate, baseArgs)
	if err != nil {
		return err
	}
	chunk := min(int64(32767), c.op.Config().Limits.MaxLOBBytes, (c.op.Config().Limits.MaxMessageBytes-1-int64(len(base)))/2)
	if chunk < 1 && len(r.data) != 0 {
		return wire.ErrMessageTooLarge
	}
	for r.result.n < len(r.data) {
		if err = c.validate(); err != nil {
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
