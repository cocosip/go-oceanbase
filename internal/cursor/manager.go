package cursor

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"slices"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// OwnRef registers a native REF CURSOR while retaining its parent statement lease.
// The lifetime coordinator publishes the returned reference only after all outputs succeed.
func (m *Manager) OwnRef(op *session.Operation, parent *statement.Statement, id uint32, columns []wire.Column, opts Options) (session.ResourceRef, error) {
	if id == 0 {
		return session.ResourceRef{}, dberrors.ErrCursor
	}
	return m.own(op, &record{refID: id, columns: slices.Clone(columns), options: opts, statement: parent, scope: op.StatementScope()})
}

func (m *Manager) own(op *session.Operation, r *record) (session.ResourceRef, error) {
	var err error
	if r.statement != nil {
		r.parent, err = r.statement.AcquireLease(op)
		if err != nil {
			return session.ResourceRef{}, err
		}
	}
	ref, err := op.Register(session.CursorResource)
	if err != nil {
		return session.ResourceRef{}, errors.Join(err, r.parent.Release(context.Background(), op))
	}
	if m.records == nil {
		m.records = make(map[session.ResourceRef]*record)
	}
	m.records[ref] = r
	return ref, nil
}

// Close retires the concrete cursor; lifetime releases its returned parent and forgets the token.
func (m *Manager) Close(ctx context.Context, op *session.Operation, ref session.ResourceRef) (*statement.Use, error) {
	r := m.records[ref]
	if r == nil {
		return nil, nil
	}
	delete(m.records, ref)
	var err error
	if r.refID != 0 && r.scope == op.StatementScope() {
		err = command.CloseStatement(ctx, op, r.refID)
	}
	return r.parent, err
}

type operation struct {
	op       *session.Operation
	resolver *catalog.Resolver
	decoder  *materialize.Decoder
	ref      session.ResourceRef
	cur      *record
}

func (m *Manager) operation(op *session.Operation, resolver *catalog.Resolver, ref session.ResourceRef) (*operation, error) {
	published, err := op.Published(ref)
	if err != nil || !published {
		return nil, dberrors.ErrResource
	}
	cur := m.records[ref]
	if cur == nil {
		return nil, dberrors.ErrResource
	}
	if resolver == nil {
		resolver = &catalog.Resolver{}
	}
	resolver.Clear()
	return &operation{op: op, resolver: resolver, decoder: materialize.New(op, resolver), ref: ref, cur: cur}, nil
}

func (o *operation) check() error { return o.op.Validate(o.ref) }

// Fetch returns a complete snapshot batch and explicitly identifies failures that retire the cursor.
func (m *Manager) Fetch(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, ref session.ResourceRef, pos Position, count int, busy *BusyToken) (driver.Rows, bool, error) {
	if err := ValidateFetch(pos, count); err != nil {
		return nil, false, err
	}
	o, err := m.operation(op, resolver, ref)
	if err != nil {
		return nil, false, err
	}
	cur := o.cur
	var rows [][]driver.Value
	switch {
	case cur.options.Mode == Static:
		rows, err = staticCursorRows(cur, pos, count)
		if err == nil {
			rows = rowset.CloneSnapshot(ctx, rows)
		}
	case cur.options.Mode == Sensitive:
		var start int64
		start, err = cursorStart(cur.position, pos)
		if err == nil && start > math.MaxInt32 {
			err = dberrors.ErrCursor
		}
		if err == nil {
			if start == 0 {
				cur.position = 0
			} else {
				rows, err = o.fetch(ctx, count, start)
				if err != nil {
					return nil, true, err
				}
			}
		}
	case pos != (Position{}):
		err = dberrors.ErrUnsupported
	case !cur.last:
		rows, err = o.fetch(ctx, count, 0)
		if err != nil {
			return nil, true, err
		}
	}
	if err != nil {
		return nil, false, err
	}
	columns := cur.columns
	if cur.options.Mode == Sensitive {
		rows, err = o.refreshSensitive(ctx, rows)
		if err != nil {
			return nil, true, err
		}
		columns = columns[:len(columns)-1]
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	if err = o.check(); err != nil {
		return nil, true, err
	}
	r := rowset.New(ctx, op.Protocol().Mode, op.Config().Session.ParseTime, []rowset.Set{{Columns: columns, Rows: rows}})
	return &batchRows{Rows: r, busy: busy}, false, nil
}

func (o *operation) fetch(ctx context.Context, count int, start int64) ([][]driver.Value, error) {
	id := o.cur.refID
	if id == 0 {
		id = o.cur.statement.ID()
	}
	set, status, result, err := o.decoder.Fetch(ctx, id, count, o.cur.columns, command.FetchOptions{Offset: o.cur.offset, Sensitive: o.cur.options.Mode == Sensitive, Start: start})
	if err != nil {
		return nil, err
	}
	if err = o.check(); err != nil {
		return nil, err
	}
	o.cur.columns = set.Columns
	o.cur.last = status&128 != 0
	if o.cur.options.Mode == Sensitive {
		if result.Affected > math.MaxInt64 {
			return nil, dberrors.ErrCursor
		}
		o.cur.position = int64(result.Affected)
	} else {
		o.cur.position += int64(len(set.Rows))
	}
	if len(set.Rows) == 0 && !o.cur.last {
		return nil, dberrors.ErrCursor
	}
	used := rowset.MetadataSize(set.Columns) + rowset.SnapshotSize(set.Rows)
	if err = o.decoder.RefCursors(ctx, ctx, &set, 0, &used, make(map[materialize.RefCursorID]bool)); err != nil {
		o.op.Discard()
		return nil, err
	}
	return set.Rows, nil
}

func (o *operation) cache(ctx context.Context) error {
	limit := o.cur.options.MaxCacheBytes
	if limit == 0 {
		limit = o.op.Config().Limits.MaxMessageBytes
	}
	o.cur.cacheBytes = rowset.MetadataSize(o.cur.columns)
	if o.cur.cacheBytes > limit {
		return wire.ErrMessageTooLarge
	}
	count := o.op.Config().Limits.FetchSize
	if count == 0 {
		count = 128
	}
	for !o.cur.last {
		rows, err := o.fetch(ctx, count, 0)
		if err != nil {
			return err
		}
		for _, row := range rows {
			o.cur.cacheBytes += rowset.SnapshotSize([][]driver.Value{row})
			if o.cur.cacheBytes > limit {
				return wire.ErrMessageTooLarge
			}
			o.cur.cache = append(o.cur.cache, row)
		}
	}
	o.cur.position = 0
	return nil
}
