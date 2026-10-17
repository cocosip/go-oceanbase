package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type cursorResource struct {
	refID      uint32
	columns    []wire.Column
	options    CursorOptions
	position   int64
	last       bool
	offset     bool
	cache      [][]driver.Value
	cacheBytes int64
	identity   *cursorIdentity
}

type cursorBatchRows struct {
	*textRows
	cursor *Cursor
}

func (r *cursorBatchRows) Close() error {
	defer r.cursor.busy.Store(false)
	return r.textRows.Close()
}

func (c *conn) execOpenCursor(ctx context.Context, request cursorOpenRequest) (_ driver.Result, err error) {
	if !validSession(request.session) || request.result == nil {
		return nil, ErrResource
	}
	if request.options.Mode == CursorSensitive && (c.session.mode != ModeOracle || !supportsPrepareExecute(c.session.obVersion)) {
		return nil, ErrUnsupported
	}
	if request.options.Updatable {
		if _, ok := request.session.(*sql.Tx); !ok || c.activeTx == nil || c.activeTx.readOnly || c.session.ok.Status&8192 != 0 {
			return nil, ErrUnsupported
		}
		if err := cursorUpdatableQuery(request.query, c.session.mode, c.session.ok.Status&512 != 0); err != nil {
			return nil, err
		}
	}
	tokens, err := lexSQL(request.query, c.session.mode, c.session.ok.Status&512 != 0, false)
	if err != nil || !cursorSingleSelect(request.query, tokens) {
		return nil, errors.Join(ErrCursor, err)
	}
	prepared, err := c.prepareServer(ctx, request.query)
	if err != nil {
		return nil, err
	}
	if err = c.lock(ctx); err != nil {
		_ = prepared.Close()
		return nil, err
	}
	defer c.unlock()
	var handle *resourceHandle
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
			defer cancel()
			if handle != nil {
				if record, e := c.resourceRecordLocked(handle); e == nil {
					err = errors.Join(err, c.releaseResourceLocked(cleanup, handle.id, record))
				}
			} else {
				err = errors.Join(err, prepared.closeLocked(cleanup))
			}
		}
	}()
	c.clearDescriptors()
	columns, last, err := c.executeCursorOpenLocked(ctx, prepared, request)
	if err != nil {
		return nil, err
	}
	cur := &cursorResource{columns: columns, last: last, options: request.options, offset: c.session.mode == ModeOracle && supportsVersion(c.session.obVersion, 2, 2, 5)}
	handle, err = c.registerResourceLocked(request.session, prepared)
	if err != nil {
		return nil, err
	}
	prepared.closeRequested = true
	c.resources.records[handle.id].cursor = cur
	if request.options.Updatable {
		if err = c.cursorIdentityLocked(ctx, cur); err != nil {
			return nil, err
		}
	}
	if cur.options.Mode == CursorStatic {
		if err = c.cacheCursorLocked(ctx, handle, cur); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = c.publishResourceLocked(handle); err != nil {
		return nil, err
	}
	*request.result = handle
	return result{known: true}, nil
}

func (c *conn) cursorParametersLocked(ctx context.Context, plan bindPlan, args []driver.NamedValue) ([]wire.Parameter, error) {
	values, err := bindArguments(plan, args)
	if err != nil {
		return nil, err
	}
	params := make([]wire.Parameter, len(values))
	budget := c.cfg.Limits.MaxMessageBytes - 10 - int64((len(values)+7)/8+1+2*len(values))
	for i, v := range values {
		limit := budget
		switch v.value.(type) {
		case Raw, []byte:
			limit = min(limit, c.cfg.Limits.MaxLOBBytes)
		}
		params[i], err = c.encodeParameterLocked(ctx, v.value, limit)
		if err != nil {
			return nil, err
		}
		budget -= params[i].EncodedSize() + int64(len(params[i].TypeMetadata))
		if budget < 0 {
			return nil, wire.ErrMessageTooLarge
		}
	}
	return params, nil
}

func (c *conn) openNativeCursorLocked(ctx context.Context, payload []byte) ([]wire.Column, bool, error) {
	r := c.startRows(ctx, true, nil)
	r.keepGate = true
	defer r.release()
	ctx = r.ctx
	c.session.packets.ResetSequence()
	if err := c.session.packets.WriteMessage(ctx, payload); err != nil {
		return nil, false, r.fail(err, !errorsPreSend(err))
	}
	data, err := c.session.packets.ReadMessage(ctx)
	if err != nil {
		return nil, false, r.fail(err, true)
	}
	if len(data) > 0 && data[0] == 255 {
		err = decodeServerError(data)
		return nil, false, r.fail(err, err == wire.ErrMalformed)
	}
	n, err := wire.DecodeColumnCount(data, maxTextColumns)
	if err != nil {
		return nil, false, r.fail(err, true)
	}
	budget := int64(len(data))
	columns, err := c.readMetadata(ctx, n, &budget, true)
	if err != nil {
		return nil, false, r.fail(err, true)
	}
	if c.session.capabilities&wire.ClientDeprecateEOF == 0 && c.session.ok.Status&(64|128) == 0 {
		return nil, false, r.fail(fmt.Errorf("%w: server did not open a cursor", ErrUnsupported), true)
	}
	return columns, c.session.capabilities&wire.ClientDeprecateEOF == 0 && c.session.ok.Status&128 != 0, nil
}

func (c *conn) queryCursorFetch(ctx context.Context, request cursorFetchRequest) (driver.Rows, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	record, err := c.resourceLocked(request.cursor.handle)
	if err != nil || record.cursor == nil {
		return nil, ErrResource
	}
	cur := record.cursor
	c.clearDescriptors()
	var rows [][]driver.Value
	if cur.options.Mode == CursorStatic {
		rows, err = staticCursorRows(cur, request.position, request.count)
		if err == nil {
			rows = cloneCursorSnapshot(ctx, rows)
		}
	} else if cur.options.Mode == CursorSensitive {
		var start int64
		start, err = cursorStart(cur.position, request.position)
		if err == nil && start > math.MaxInt32 {
			err = ErrCursor
		}
		if err == nil {
			if start == 0 {
				cur.position = 0
			} else {
				rows, err = c.fetchNativeCursorLocked(ctx, request.cursor.handle, cur, request.count, start)
				if err != nil {
					return nil, c.invalidateCursorLocked(request.cursor, err)
				}
			}
		}
	} else if request.position != (CursorPosition{}) {
		err = ErrUnsupported
	} else if !cur.last {
		rows, err = c.fetchNativeCursorLocked(ctx, request.cursor.handle, cur, request.count, 0)
		if err != nil {
			return nil, c.invalidateCursorLocked(request.cursor, err)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
		return nil, c.invalidateCursorLocked(request.cursor, err)
	}
	r := &textRows{conn: c, ctx: ctx, iterationCtx: ctx, columns: cur.columns, released: true, buffered: []lobRowset{{columns: cur.columns, rows: rows}}}
	return &cursorBatchRows{textRows: r, cursor: request.cursor}, nil
}

func (c *conn) fetchNativeCursorLocked(ctx context.Context, handle *resourceHandle, cur *cursorResource, count int, start int64) ([][]driver.Value, error) {
	record, err := c.resourceLocked(handle)
	if err != nil {
		return nil, err
	}
	id := record.parent.id
	if cur.refID != 0 {
		id = cur.refID
	}
	rows, err := c.fetchCursorResponseLocked(ctx, id, cur, count, start)
	if err != nil {
		return nil, err
	}
	set := lobRowset{columns: cur.columns, rows: rows}
	used := rowsetMetadataSize(cur.columns) + cursorSnapshotSize(rows)
	if err = c.materializeRefCursorsLocked(ctx, ctx, &set, 0, &used, make(map[refCursorID]bool)); err != nil {
		c.discard()
		return nil, err
	}
	return set.rows, nil
}

func (c *conn) fetchCursorResponseLocked(ctx context.Context, id uint32, cur *cursorResource, count int, start int64) ([][]driver.Value, error) {
	var err error
	payload := binary.LittleEndian.AppendUint32([]byte{28}, id)
	payload = binary.LittleEndian.AppendUint32(payload, uint32(count))
	if cur.offset {
		orientation, offset := uint16(2), uint32(0)
		if cur.options.Mode == CursorSensitive {
			orientation, offset = 32, uint32(start)
		}
		payload = binary.LittleEndian.AppendUint16(payload, orientation)
		payload = binary.LittleEndian.AppendUint32(payload, offset)
		payload = binary.LittleEndian.AppendUint32(payload, 1)
	}
	r := c.startRows(ctx, true, nil)
	r.keepGate, r.holdRelease, r.captureLOB = true, true, true
	ctx = r.ctx
	c.session.packets.ResetSequence()
	if err = c.session.packets.WriteMessage(ctx, payload); err != nil {
		r.holdRelease = false
		return nil, r.fail(err, !errorsPreSend(err))
	}
	if c.session.mode == ModeOracle {
		if err = r.advance(ctx); err != nil {
			r.holdRelease = false
			return nil, r.fail(err, true)
		}
		r.nativeTail = cur.offset
	} else {
		r.columns, r.done = cur.columns, false
	}
	sets, err := r.collectLOBResponse(ctx)
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	if len(sets) != 1 || len(sets[0].rows)+len(sets[0].rawRows) > count || cur.columns != nil && len(sets[0].columns) != len(cur.columns) {
		return nil, r.fail(ErrCursor, true)
	}
	err = r.materializeCursorSetLocked(ctx, &sets[0])
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.releaseWithContext(ctx)
	if r.err != nil {
		return nil, r.err
	}
	cur.columns = sets[0].columns
	cur.last = r.lastRowStatus&128 != 0
	if cur.options.Mode == CursorSensitive {
		if r.last.affected > math.MaxInt64 {
			return nil, ErrCursor
		}
		cur.position = int64(r.last.affected)
	} else {
		cur.position += int64(len(sets[0].rows))
	}
	if len(sets[0].rows) == 0 && !cur.last {
		return nil, ErrCursor
	}
	return sets[0].rows, nil
}

func (c *conn) cacheCursorLocked(ctx context.Context, handle *resourceHandle, cur *cursorResource) error {
	limit := cur.options.MaxCacheBytes
	if limit == 0 {
		limit = c.cfg.Limits.MaxMessageBytes
	}
	cur.cacheBytes = rowsetMetadataSize(cur.columns)
	if cur.cacheBytes > limit {
		return wire.ErrMessageTooLarge
	}
	count := c.cfg.Limits.FetchSize
	if count == 0 {
		count = 128
	}
	for !cur.last {
		rows, err := c.fetchNativeCursorLocked(ctx, handle, cur, count, 0)
		if err != nil {
			return err
		}
		for _, row := range rows {
			cur.cacheBytes += cursorSnapshotSize([][]driver.Value{row})
			if cur.cacheBytes > limit {
				return wire.ErrMessageTooLarge
			}
			cur.cache = append(cur.cache, row)
		}
	}
	cur.position = 0
	return nil
}

func staticCursorRows(cur *cursorResource, pos CursorPosition, count int) ([][]driver.Value, error) {
	start, err := cursorStart(cur.position, pos)
	if err != nil {
		return nil, err
	}
	if start == 0 {
		cur.position = 0
		return nil, nil
	}
	if start > int64(len(cur.cache)) {
		cur.position = int64(len(cur.cache)) + 1
		return nil, nil
	}
	n := min(int64(count), int64(len(cur.cache))-start+1)
	cur.position = start + n - 1
	return cur.cache[start-1 : start-1+n], nil
}

func cursorStart(current int64, pos CursorPosition) (int64, error) {
	offset := pos.Offset
	switch pos.Origin {
	case CursorAbsolute:
		return offset, nil
	case CursorNext:
		offset = 1
	}
	if offset > 0 && current > math.MaxInt64-offset || offset < 0 && offset < -current {
		return 0, ErrCursor
	}
	return current + offset, nil
}

func (c *conn) openSensitiveCursorLocked(ctx context.Context, payload []byte, prepared *stmt) ([]wire.Column, error) {
	r := c.startRows(ctx, true, nil)
	r.keepGate, r.holdRelease, r.captureLOB = true, true, true
	ctx = r.ctx
	c.session.packets.ResetSequence()
	if err := c.session.packets.WriteMessage(ctx, payload); err != nil {
		r.holdRelease = false
		return nil, r.fail(err, !errorsPreSend(err))
	}
	data, err := c.session.packets.ReadMessage(ctx)
	if err != nil {
		r.holdRelease = false
		return nil, r.fail(err, true)
	}
	if len(data) > 0 && data[0] == 255 {
		r.holdRelease = false
		err = decodeServerError(data)
		return nil, r.fail(err, err == wire.ErrMalformed)
	}
	header, err := wire.DecodePrepareExecuteOK(data)
	if err != nil || header.ID != prepared.id || int(header.Parameters) != len(prepared.plan.slots) || !header.HasRows || header.Flags != 0 {
		r.holdRelease = false
		return nil, r.fail(wire.ErrMalformed, true)
	}
	budget := int64(len(data))
	if _, err = c.prepareMetadata(ctx, int(header.Parameters), &budget); err == nil {
		err = r.readColumns(ctx, int(header.Columns), budget)
	}
	if err != nil {
		r.holdRelease = false
		return nil, r.fail(err, true)
	}
	r.nativeTail = true
	sets, err := r.collectLOBResponse(ctx)
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	if len(sets) != 1 {
		return nil, r.fail(ErrCursor, true)
	}
	// Consume and release locator resources even though the first reposition replaces these rows.
	err = r.materializeCursorSetLocked(ctx, &sets[0])
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.releaseWithContext(ctx)
	return sets[0].columns, r.err
}

func cursorSingleSelect(query string, tokens []sqlToken) bool {
	if len(tokens) == 0 || !strings.EqualFold(query[tokens[0].start:tokens[0].end], "SELECT") {
		return false
	}
	for i, t := range tokens {
		if t.kind == ';' && i != len(tokens)-1 {
			return false
		}
	}
	return true
}

func (c *conn) executeCursorOpenLocked(ctx context.Context, prepared *stmt, request cursorOpenRequest) ([]wire.Column, bool, error) {
	if request.options.Mode == CursorSensitive {
		values, err := bindArguments(prepared.plan, request.args)
		if err != nil {
			return nil, false, err
		}
		payload, err := c.outputPrepareExecute(ctx, prepared.plan, values, prepared)
		if err != nil {
			return nil, false, err
		}
		payload[5] = 1
		binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
		if err = c.uploadLongDataLocked(ctx, prepared.id, values); err != nil {
			return nil, false, err
		}
		columns, err := c.openSensitiveCursorLocked(ctx, payload, prepared)
		return columns, false, err
	}
	params, err := c.cursorParametersLocked(ctx, prepared.plan, request.args)
	if err != nil {
		return nil, false, err
	}
	payload, err := wire.EncodeStmtExecute(prepared.id, params, c.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, false, err
	}
	payload[5] = 1
	values, err := bindArguments(prepared.plan, request.args)
	if err != nil {
		return nil, false, err
	}
	if err = c.uploadLongDataLocked(ctx, prepared.id, values); err != nil {
		return nil, false, err
	}
	return c.openNativeCursorLocked(ctx, payload)
}
