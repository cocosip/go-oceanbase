package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const maxTextColumns = 4096

type textRows struct {
	conn                 *conn
	ctx                  context.Context
	iterationCtx         context.Context
	columns              []wire.Column
	done, more, released bool
	items                int
	last                 result
	lastRowStatus        uint16
	err                  error
	stopCancel           func() bool
	cancelDone           chan struct{}
	stopTx               func()
	binary               bool
	autoStmt             *stmt
	parameterStmt        *stmt
	execution            *Execution
	keepGate             bool
	outputs              *outputState
	nativeTail           bool
	hiddenOut            bool
	captureLOB           bool
	holdRelease          bool
	buffered             []lobRowset
	bufferRow            int
	bufferBytes          int64
	localInfile          *LocalInfile
	localInfileAck       bool
	cancelExecution      bool
}

func (r *textRows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, c := range r.columns {
		names[i] = c.Name
	}
	return names
}

func (r *textRows) release() { r.releaseWithContext(r.ctx) }

func (r *textRows) releaseWithContext(opCtx context.Context) {
	if r.cancelExecution {
		r.conn.endCancelExecution(r.contextError() != nil)
		r.cancelExecution = false
	}
	if r.holdRelease {
		r.conn.responseActive = false
		return
	}
	if !r.released {
		r.acknowledgeBatch()
		if cause := r.contextError(); cause != nil {
			// A queued discard callback may be stopped below; canceled commands still poison state.
			r.conn.discard()
			if r.err == nil {
				r.err = cause
			}
		}
		r.released = true
		r.conn.responseActive = false
		if r.stopCancel != nil && !r.stopCancel() {
			<-r.cancelDone
		}

		if r.autoStmt != nil && r.outputs != nil && !r.outputs.hasResources() {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			r.err = errors.Join(r.err, r.autoStmt.closeLocked(ctx))
			cancel()
		}
		r.finalizeOutputsLocked(opCtx)
		if r.autoStmt != nil && r.outputs.hasResources() {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			if err := r.autoStmt.closeLocked(ctx); err != nil && r.err == nil {
				r.err = err
			}
			cancel()
		}
		if cause := r.contextError(); cause != nil && !errors.Is(r.err, cause) {
			r.err = errors.Join(r.err, cause)
			r.conn.discard()
		}
		if !r.keepGate {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			r.err = errors.Join(r.err, r.conn.cleanupResourcesLocked(ctx, false, r.outputs.pendingHandles()...))
			cancel()
		}
		if r.outputs != nil && !r.outputs.internal && r.err == nil {
			r.err = r.conn.assignOutputsContext(opCtx, r.outputs.bindings, r.outputs.values)
		}

		if r.err != nil && !r.keepGate {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			r.err = errors.Join(r.err, r.conn.cleanupResourcesLocked(ctx, false))
			cancel()
		}

		if r.stopTx != nil {
			r.stopTx()
		}
		if r.autoStmt != nil && r.outputs == nil {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			r.err = errors.Join(r.err, r.conn.recycleStatementLocked(ctx, r.autoStmt, r.err == nil))
			cancel()
		}
		r.execution.finish(r.err)
		if s := r.parameterStmt; s != nil {
			s.lastTypes = nil
			if r.err == nil {
				s.lastTypes = s.pendingTypes
			}
			s.pendingTypes = nil
		}
		if !r.keepGate {
			r.conn.unlock()
		}
	}
}
func (r *textRows) finalizeOutputsLocked(opCtx context.Context) {

	if r.outputs != nil {
		if err := r.outputs.checkResourceKeyLocked(r.conn); err != nil {
			r.err = errors.Join(r.err, err)
			// Deferred complex rows cannot enumerate child resources without more server queries.
			if len(r.outputs.complexRows) != 0 {
				r.conn.discard()
			}
		}
		if r.err == nil {
			r.err = r.outputs.decodeComplexLocked(opCtx, r.conn)
			if r.err != nil {
				r.conn.discard()
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
		r.err = errors.Join(r.err, r.outputs.finishLocked(ctx, r.conn, r.err == nil))
		cancel()
		if !r.outputs.internal && !r.conn.closed.Load() && (r.err == nil || r.outputs.hasResources() && r.outputs.seen) {
			if err := r.outputs.prepareResourcesLocked(opCtx, r); err != nil {
				r.err = errors.Join(r.err, err)
				r.conn.discard()
			}
		}
	}
}

func (r *textRows) fail(err error, broken bool) error {
	if !r.released {
		if cause := r.contextError(); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}
	if r.err == nil {
		r.err = err
	}
	if !r.released && broken {
		r.conn.discard()
	}
	r.release()
	return r.err
}

func (r *textRows) contextError() error {
	if err := context.Cause(r.ctx); err != nil {
		return err
	}
	if r.conn.activeTx != nil {
		return context.Cause(r.conn.activeTx.ctx)
	}
	return nil
}

// advance skips command-only results so NextResultSet exposes ordinary rowsets only.
func (r *textRows) advance(ctx context.Context) error {
	for {
		data, err := r.conn.session.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return wire.ErrMalformed
		}
		if r.localInfileAck {
			r.localInfileAck = false
			if data[0] != 0 && data[0] != 255 {
				return wire.ErrMalformed
			}
		}
		if data[0] == 251 {
			if err = r.uploadLocalInfile(ctx, data); err != nil {
				return err
			}
			r.localInfileAck = true
			continue
		}
		if data[0] == 255 {
			err = decodeServerError(data)
			var server *Error
			if errors.As(err, &server) {
				if r.execution != nil && r.items < r.conn.cfg.Limits.MaxResultItems {
					r.execution.add(ResultInfo{Kind: ResultFailure, Complete: true, Err: err})
				}
				return r.fail(err, false)
			}
			return err
		}
		if r.nativeTail {
			if data[0] != 0 {
				return wire.ErrMalformed
			}
			r.nativeTail = false
		}
		r.items++
		if r.items > r.conn.cfg.Limits.MaxResultItems {
			return fmt.Errorf("oceanbase: result item limit exceeded")
		}
		if data[0] == 0 {
			ok, err := wire.DecodeOK(data, r.conn.session.capabilities)
			if err != nil {
				return err
			}
			r.last = result{affected: ok.AffectedRows, id: ok.InsertID, known: true}
			r.conn.observeResourceStatus(ok.Status)
			r.conn.session.ok = ok
			if !r.keepGate {
				r.conn.lastWarnings = WarningSummary{Count: ok.Warnings, Known: true}
			}
			if err = r.execution.command(ok); err != nil {
				return err
			}
			r.more = ok.Status&8 != 0
			r.done = true
			r.columns = nil
			if !r.more {
				r.releaseWithContext(ctx)
				return r.err
			}
			continue
		}
		n, err := wire.DecodeColumnCount(data, maxTextColumns)
		if err != nil {
			return err
		}
		if err = r.readColumns(ctx, n, int64(len(data))); err != nil {
			return err
		}
		if r.hiddenOut {
			if err = r.readOutputs(ctx); err != nil {
				return err
			}
			if !r.more {
				r.releaseWithContext(ctx)
				return r.err
			}
			continue
		}
		return nil
	}
}

func (r *textRows) readColumns(ctx context.Context, count int, budget int64) error {
	if count < 1 || count > maxTextColumns {
		return wire.ErrMessageTooLarge
	}
	columns, err := r.conn.readMetadata(ctx, count, &budget, true)
	if err != nil {
		return err
	}
	if r.outputs != nil && r.conn.session.capabilities&wire.ClientDeprecateEOF == 0 && r.conn.session.ok.Status&4096 != 0 {
		r.hiddenOut = true
	}
	r.columns, r.done, r.more = columns, false, false
	if !r.hiddenOut {
		return r.execution.rowset(columns, r.conn.cfg.Limits.MaxMessageBytes)
	}
	return nil
}

func (r *textRows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.buffered != nil {
		if err := r.iterationCtx.Err(); err != nil {
			return err
		}
		if r.bufferRow >= len(r.buffered[0].rows) {
			return io.EOF
		}
		copy(dest, r.buffered[0].rows[r.bufferRow])
		r.bufferRow++
		return nil
	}
	if r.done {
		return io.EOF
	}
	if len(dest) != len(r.columns) {
		return r.fail(wire.ErrMalformed, true)
	}
	return r.next(r.ctx, dest)
}

func (r *textRows) next(ctx context.Context, dest []driver.Value) error {
	data, err := r.rowPacket(ctx)
	if err != nil {
		return err
	}
	return r.decodeRow(data, dest)
}

func (r *textRows) rowPacket(ctx context.Context) ([]byte, error) {
	data, err := r.conn.session.packets.ReadMessage(ctx)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if r.holdRelease {
		r.bufferBytes += int64(len(data))
		if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return nil, r.fail(wire.ErrMessageTooLarge, true)
		}
	}
	if len(data) == 0 {
		return nil, r.fail(wire.ErrMalformed, true)
	}
	if data[0] == 255 {
		err = decodeServerError(data)
		var server *Error
		return nil, r.fail(err, !errors.As(err, &server))
	}
	if data[0] == 254 && (len(data) < 9 || (r.conn.session.capabilities&wire.ClientDeprecateEOF != 0 && len(data) < 0xffffff)) {
		var ok wire.OK
		if r.conn.session.capabilities&wire.ClientDeprecateEOF != 0 {
			ok, err = wire.DecodeOK(data, r.conn.session.capabilities)
		} else {
			ok, err = wire.DecodeEOF(data)
		}
		if err != nil {
			return nil, r.fail(err, true)
		}
		r.conn.observeResourceStatus(ok.Status)
		r.conn.session.ok = ok
		if !r.keepGate {
			r.conn.lastWarnings = WarningSummary{Count: ok.Warnings, Known: true}
		}
		r.done = true
		r.lastRowStatus = ok.Status
		if !r.hiddenOut {
			r.execution.rowDone(ok.Warnings)
		}
		r.more = ok.Status&8 != 0 || r.nativeTail
		if !r.more && !r.hiddenOut {
			r.releaseWithContext(ctx)
		}
		if r.err != nil {
			return nil, r.err
		}
		return nil, io.EOF
	}
	return data, nil
}

func (r *textRows) decodeRow(data []byte, dest []driver.Value) error {
	var fields [][]byte
	var err error
	if r.binary {
		fields, err = wire.DecodeBinaryRow(data, r.columns)
	} else {
		fields, err = wire.DecodeTextRow(data, len(r.columns))
	}
	if err != nil {
		return r.fail(err, true)
	}
	if dest != nil {
		for i, field := range fields {
			if r.columns[i].Type == 163 {
				if r.conn.session.mode != ModeOracle || !r.binary {
					return r.fail(ErrUnsupported, true)
				}
				if field != nil {
					id := binary.LittleEndian.Uint32(field)
					if id == 0 {
						return r.fail(ErrCursor, true)
					}
					dest[i] = refCursorID(id)
				}
				continue
			}
			if r.columns[i].Type == 160 {
				if !r.binary {
					return r.fail(fmt.Errorf("%w: complex rows require server prepared statements", ErrUnsupported), true)
				}
				if field != nil {
					dest[i] = append([]byte{}, field...)
				}
				continue
			}
			if (r.captureLOB || r.hiddenOut) && r.conn.session.mode == ModeOracle {
				if value, ok := captureLOBValue(r.columns[i], field); ok {
					dest[i] = value
					continue
				}
			}
			if typ := r.columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > r.conn.cfg.Limits.MaxLOBBytes {
				return r.fail(wire.ErrMessageTooLarge, true)
			}
			dest[i], err = r.conn.decodeRowValue(r.columns[i], r.binary, field)
			if err != nil {
				return r.fail(err, true)
			}
		}
	}
	return nil
}

func (r *textRows) drainCurrent(ctx context.Context) error {
	for !r.done {
		if err := r.next(ctx, nil); err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}

func (r *textRows) drainAll(ctx context.Context) error {
	if r.err != nil {
		return r.err
	}
	if r.released {
		return r.err
	}
	for {
		if err := r.bufferLOBResponse(ctx); err != nil {
			return err
		}
		if r.released {
			r.buffered = nil
			return r.err
		}
		if err := r.drainCurrent(ctx); err != nil {
			return err
		}
		if !r.more {
			r.releaseWithContext(ctx)
			return r.err
		}
		if err := r.advance(ctx); err != nil {
			return r.fail(err, true)
		}
		if r.released {
			return r.err
		}
	}
}

func (r *textRows) Close() error {
	if r.buffered != nil {
		r.buffered = nil
		return r.err
	}
	if r.released {
		return r.err
	}
	if err := r.contextError(); err != nil {
		return r.fail(err, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
	defer cancel()
	return r.drainAll(ctx)
}

func (r *textRows) HasNextResultSet() bool {
	if r.buffered != nil {
		return len(r.buffered) > 1
	}
	return !r.released && (r.more || !r.done)
}
func (r *textRows) NextResultSet() error {
	if r.err != nil {
		return r.err
	}
	if r.buffered != nil {
		if err := r.iterationCtx.Err(); err != nil {
			return err
		}
		if len(r.buffered) <= 1 {
			return io.EOF
		}
		r.buffered[0] = lobRowset{}
		r.buffered = r.buffered[1:]
		r.bufferRow, r.columns = 0, r.buffered[0].columns
		return nil
	}
	if r.released {
		return io.EOF
	}
	if err := r.drainCurrent(r.ctx); err != nil {
		return err
	}
	if !r.more {
		return io.EOF
	}
	if err := r.advance(r.ctx); err != nil {
		return r.fail(err, true)
	}
	if r.released {
		return io.EOF
	}
	if err := r.bufferLOBResponse(r.ctx); err != nil {
		return err
	}
	return nil
}

func (r *textRows) ColumnTypeDatabaseTypeName(index int) string {
	return textTypeName(r.columns[index], r.conn.session.mode)
}
func (r *textRows) ColumnTypeNullable(index int) (bool, bool) {
	return r.columns[index].Flags&1 == 0, true
}
func (r *textRows) ColumnTypeLength(index int) (int64, bool) {
	return columnLength(r.columns[index], r.conn.session.mode)
}
func (r *textRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	return columnPrecisionScale(r.columns[index], r.conn.session.mode)
}

func columnLength(c wire.Column, mode Mode) (int64, bool) {
	if c.NewMetadata && mode == ModeOracle {
		switch c.Type {
		case 203, 209:
			return int64(c.Precision), c.Precision != 0
		case 210, 211:
			return 0, false
		}
	}
	if c.Length == 0 {
		return 0, false
	}
	if c.NewMetadata && mode == ModeOracle && (c.Type == 207 || c.Type == 208) {
		return int64(c.Length), true
	}
	if c.Type == 203 || c.Type == 16 || c.Type >= 249 && c.Type <= 254 && c.Charset == 63 {
		return int64(c.Length), true
	}
	switch c.Type {
	case 15, 253, 254, 207, 208, 249, 250, 251, 252:
		if mode == ModeOracle && (c.Type == 15 || c.Type == 253 || c.Type == 254) {
			return int64(c.Length), true
		}
		name, ok := charset.Collation(c.Charset)
		// HKSCS metadata uses the server's mbmaxlen=2 despite its UTF-8 wire payload.
		width := map[string]uint32{"binary": 1, "ascii": 1, "latin1": 1, "gbk": 2, "hkscs": 2, "hkscs31": 2, "utf8mb3": 3, "utf8mb4": 4, charsetUTF16: 4, "gb18030": 4, "gb18030_2022": 4}[name]
		if !ok || width == 0 || c.Length%width != 0 {
			return 0, false
		}
		return int64(c.Length / width), true
	}
	return 0, false
}

func columnPrecisionScale(c wire.Column, mode Mode) (int64, int64, bool) {
	if c.Type != 0 && c.Type != 246 && (mode != ModeOracle || c.Type != 3) {
		return 0, 0, false
	}
	if mode == ModeOracle {
		if c.Precision == 0 || c.Precision > 38 || int8(c.Decimals) < -84 {
			return 0, 0, false
		}
		return int64(c.Precision), int64(int8(c.Decimals)), true
	}
	if c.Decimals > 30 {
		return 0, 0, false
	}
	p := int64(c.Length)
	if c.Flags&32 == 0 {
		p--
	}
	if c.Decimals > 0 {
		p--
	}
	if p <= 0 || p > 65 || p < int64(c.Decimals) {
		return 0, 0, false
	}
	return p, int64(c.Decimals), true
}
func (r *textRows) ColumnTypeScanType(index int) reflect.Type {
	c := r.columns[index]
	if c.Type == 163 && r.conn.session.mode == ModeOracle {
		return reflect.TypeFor[sql.Rows]()
	}
	if c.Type == 210 && r.conn.session.mode == ModeOracle {
		return reflect.TypeFor[[]byte]()
	}
	if r.conn.cfg.Session.ParseTime && (c.Type == 7 || c.Type == 10 || c.Type == 12 || c.Type == 14 || c.Type == 202 && r.conn.session.mode == ModeOracle) {
		return reflect.TypeFor[time.Time]()
	}
	if c.Charset == 63 && (c.Type == 15 || c.Type >= 249) || c.Type == 16 || c.Type == 203 {
		return reflect.TypeFor[[]byte]()
	}
	switch c.Type {
	case 8:
		if c.Flags&32 != 0 {
			return reflect.TypeFor[uint64]()
		}
		return reflect.TypeFor[int64]()
	case 1, 2, 9, 13:
		return reflect.TypeFor[int64]()
	case 3:
		if r.conn.session.mode != ModeOracle {
			return reflect.TypeFor[int64]()
		}
	case 4, 5:
		return reflect.TypeFor[float64]()
	}
	return reflect.TypeFor[string]()
}

var (
	_ driver.Rows                           = (*textRows)(nil)
	_ driver.RowsNextResultSet              = (*textRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*textRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*textRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*textRows)(nil)
	_ driver.RowsColumnTypeLength           = (*textRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*textRows)(nil)
)
