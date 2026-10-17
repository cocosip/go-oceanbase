package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// CursorOut returns a forward REF CURSOR from Execute. Dest must initially point to nil.
// Consume or close Execution before using the published cursor.
type CursorOut struct{ Dest **Cursor }

type refCursorID uint32

func makeResourceOutput(out Out) (*outBinding, bool, error) {
	var typ byte
	switch out.Dest.(type) {
	case **Cursor:
		typ = 163
	case **Blob:
		typ = 210
	case **Clob:
		typ = 211
	default:
		return nil, false, nil
	}
	name := strings.ToUpper(strings.TrimSpace(out.Type))
	expected := name == typeNameRefCursor && typ == 163 || name == typeNameBlob && typ == 210 || (name == typeNameClob || name == typeNameNClob) && typ == 211
	v := reflect.ValueOf(out.Dest)
	if !expected || v.IsNil() || !v.Elem().IsNil() || out.In || out.Length != 0 || out.Precision != 0 || out.Scale != 0 {
		return nil, true, ErrOut
	}
	out.Type = name
	return &outBinding{Out: out, resource: typ}, true, nil
}

func (s *outputState) hasResources() bool {
	if s != nil {
		for _, b := range s.bindings {
			if b != nil && b.resource != 0 {
				return true
			}
		}
	}
	return false
}
func (s *outputState) pendingHandles() []*resourceHandle {
	var handles []*resourceHandle
	if s != nil {
		for _, b := range s.bindings {
			if b != nil && b.handle != nil {
				handles = append(handles, b.handle)
			}
		}
	}
	return handles
}

// Register every lease before requesting the temporary parent statement's close.
func (s *outputState) prepareResourcesLocked(ctx context.Context, r *textRows) error {
	c := r.conn
	if !s.hasResources() {
		return c.materializeLOBValuesLocked(ctx, s.columns, s.values, c.cfg.Limits.MaxMessageBytes)
	}
	columns := append([]wire.Column(nil), s.columns...)
	if len(columns) != len(s.values) {
		return ErrOut
	}
	seen := make(map[refCursorID]bool)
	for i, b := range s.bindings {
		if b.resource == 0 {
			continue
		}
		if columns[i].Type != b.resource {
			return ErrOut
		}
		columns[i].Type = 0
		if s.values[i] == nil {
			continue
		}
		e := r.execution
		if e == nil || !validSession(e.session) || s.parent == nil {
			return ErrResource
		}
		var cur *cursorResource
		var lob *nativeLOBResource
		if b.resource == 163 {
			id, ok := s.values[i].(refCursorID)
			if !ok || uint32(id) == s.parent.id || seen[id] {
				return ErrOut
			}
			seen[id] = true
			cur = &cursorResource{refID: uint32(id)}
		} else {
			raw, ok := s.values[i].([]byte)
			if !ok {
				return ErrLOB
			}
			locator, err := wire.DecodeLOBLocator(raw)
			if err != nil || locator.ByteLength > math.MaxInt64 {
				return errors.Join(ErrLOB, err)
			}
			lob = &nativeLOBResource{raw: raw, locator: locator, column: s.columns[i]}
		}
		h, err := c.registerResourceLocked(e.session, s.parent)
		if err != nil {
			return err
		}
		b.handle = h
		record := c.resources.records[h.id]
		record.cursor, record.lob = cur, lob
		switch b.resource {
		case 163:
			s.values[i] = &Cursor{handle: h}
		case 210:
			s.values[i] = &Blob{handle: h}
		case 211:
			s.values[i] = &Clob{handle: h}
		}
	}
	if r.err != nil {
		return nil
	}
	return c.materializeLOBValuesLocked(ctx, columns, s.values, c.cfg.Limits.MaxMessageBytes)
}

// Ordinary child Rows are bounded snapshots: fetch only after the parent's full response.
func (c *conn) materializeRefCursorsLocked(ctx, iterationCtx context.Context, set *lobRowset, depth int, used *int64, active map[refCursorID]bool) error {
	for _, row := range set.rows {
		for i, column := range set.columns {
			if column.Type != 163 || row[i] == nil {
				continue
			}
			id, ok := row[i].(refCursorID)
			if !ok || id == 0 || depth >= 16 || active[id] {
				return ErrCursor
			}
			active[id] = true
			child, err := c.materializeRefCursorLocked(ctx, iterationCtx, id, depth, used, active)
			delete(active, id)
			if err != nil {
				return err
			}
			row[i] = child
		}
	}
	return nil
}

func (c *conn) materializeRefCursorLocked(ctx, iterationCtx context.Context, id refCursorID, depth int, used *int64, active map[refCursorID]bool) (_ driver.Rows, err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, (&stmt{conn: c, id: uint32(id)}).closeLocked(cleanup))
	}()
	cur := &cursorResource{refID: uint32(id)}
	set := lobRowset{}
	count := c.cfg.Limits.FetchSize
	if count == 0 {
		count = 128
	}
	for !cur.last {
		rows, e := c.fetchCursorResponseLocked(ctx, uint32(id), cur, count, 0)
		if e != nil {
			return nil, e
		}
		if set.columns == nil {
			set.columns = cur.columns
			*used += rowsetMetadataSize(set.columns)
		}
		for _, row := range rows {
			*used += 24 + int64(len(row))*16 + lobValuesSize(row)
		}
		if *used > c.cfg.Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
		batch := lobRowset{columns: cur.columns, rows: rows}
		if e = c.materializeRefCursorsLocked(ctx, iterationCtx, &batch, depth+1, used, active); e != nil {
			return nil, e
		}
		set.rows = append(set.rows, batch.rows...)
	}
	return &textRows{conn: c, ctx: iterationCtx, iterationCtx: iterationCtx, columns: set.columns, released: true, buffered: []lobRowset{set}}, nil
}

func cursorSnapshotSize(rows [][]driver.Value) int64 {
	var n int64
	for _, row := range rows {
		n += 24 + int64(len(row))*16 + lobValuesSize(row)
		for _, value := range row {
			if child, ok := value.(*textRows); ok {
				for _, set := range child.buffered {
					n += rowsetMetadataSize(set.columns) + cursorSnapshotSize(set.rows)
				}
			}
		}
	}
	return n
}

// Static cache owns immutable snapshots; each Fetch gets independent child readers.
func cloneCursorSnapshot(ctx context.Context, rows [][]driver.Value) [][]driver.Value {
	cloned := make([][]driver.Value, len(rows))
	for i, row := range rows {
		cloned[i] = append([]driver.Value(nil), row...)
		for j, value := range row {
			switch v := value.(type) {
			case []byte:
				cloned[i][j] = append([]byte{}, v...)
			case *textRows:
				sets := make([]lobRowset, len(v.buffered))
				for k, set := range v.buffered {
					sets[k] = lobRowset{columns: set.columns, rows: cloneCursorSnapshot(ctx, set.rows)}
				}
				cloned[i][j] = &textRows{conn: v.conn, ctx: ctx, iterationCtx: ctx, columns: v.columns, released: true, buffered: sets}
			}
		}
	}
	return cloned
}
