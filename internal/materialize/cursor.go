package materialize

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// RefCursors creates bounded child snapshots after the enclosing response has ended.
func (c *Decoder) RefCursors(ctx, iterationCtx context.Context, set *Set, depth int, used *int64, active map[RefCursorID]bool) error {
	for _, row := range set.Rows {
		for i, column := range set.Columns {
			if column.Type != 163 || row[i] == nil {
				continue
			}
			id, ok := row[i].(RefCursorID)
			if !ok || id == 0 || depth >= 16 || active[id] {
				return dberrors.ErrCursor
			}
			active[id] = true
			child, err := c.refCursor(ctx, iterationCtx, id, depth, used, active)
			delete(active, id)
			if err != nil {
				return err
			}
			row[i] = child
		}
	}
	return nil
}

func (c *Decoder) refCursor(ctx, iterationCtx context.Context, id RefCursorID, depth int, used *int64, active map[RefCursorID]bool) (_ driver.Rows, err error) {
	scope := c.op.StatementScope()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.op.Config().Limits.CleanupTimeout)
		defer cancel()
		if scope == c.op.StatementScope() {
			err = errors.Join(err, command.CloseStatement(cleanup, c.op, uint32(id)))
		}
	}()
	var set Set
	count := c.op.Config().Limits.FetchSize
	if count == 0 {
		count = 128
	}
	for {
		batch, status, _, e := c.Fetch(ctx, uint32(id), count, set.Columns, command.FetchOptions{})
		if e != nil {
			return nil, e
		}
		if set.Columns == nil {
			set.Columns = batch.Columns
			*used += valuecodec.SnapshotMetadataSize(set.Columns)
		}
		for _, row := range batch.Rows {
			*used += 24 + int64(len(row))*16 + valuecodec.SnapshotValuesSize(row)
		}
		if *used > c.op.Config().Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
		if e = c.RefCursors(ctx, iterationCtx, &batch, depth+1, used, active); e != nil {
			return nil, e
		}
		set.Rows = append(set.Rows, batch.Rows...)
		last := status&128 != 0
		if len(batch.Rows) == 0 && !last {
			return nil, dberrors.ErrCursor
		}
		if last {
			break
		}
	}
	return rowset.New(iterationCtx, c.op.Protocol().Mode, c.op.Config().Session.ParseTime, []rowset.Set{{Columns: set.Columns, Rows: set.Rows}}), nil
}
