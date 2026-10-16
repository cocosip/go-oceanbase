package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type nativeLOBResource struct {
	raw     []byte
	locator wire.LOBLocator
	column  wire.Column
}

type lobOpenContextKey struct{}
type lobOpenState struct{ gate bool }

func (c *conn) execOpenLOB(ctx context.Context, request lobOpenRequest) (_ driver.Result, err error) {
	if !validSession(request.session) || request.result == nil || request.typ != 210 && request.typ != 211 {
		return nil, ErrLOB
	}
	if c.session.mode != ModeOracle {
		return nil, ErrUnsupported
	}
	state := &lobOpenState{}
	var selected *resourceHandle
	defer func() {
		if !state.gate {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, c.cleanupResourcesLocked(cleanup, false))
		if err == nil {
			err = ctx.Err()
		}
		if err != nil && selected != nil {
			if record, e := c.resourceRecordLocked(selected); e == nil {
				err = errors.Join(err, c.releaseResourceLocked(cleanup, selected.id, record))
			}
		}
		if err == nil {
			*request.result = selected
		}
		c.unlock()
	}()
	rows, err := c.QueryContext(context.WithValue(ctx, lobOpenContextKey{}, state), request.query, request.args)
	if err != nil {
		return nil, err
	}
	r := rows.(*textRows)
	r.holdRelease = true
	sets, err := r.collectLOBResponse()
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.release()
	if r.err != nil {
		return nil, r.err
	}
	handles, err := c.registerLOBRowsLocked(request.session, sets)
	if err != nil {
		return nil, err
	}

	if len(sets) != 1 || len(sets[0].columns) != 1 || sets[0].columns[0].Type != request.typ {
		return nil, ErrLOB
	}
	if len(sets[0].rows) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(sets[0].rows) != 1 {
		return nil, ErrLOB
	}
	if sets[0].rows[0][0] != nil {
		selected = handles[0]
		if err = c.publishResourceLocked(selected); err != nil {
			return nil, err
		}
	}
	return result{known: true}, nil
}

func (c *conn) execLOB(ctx context.Context, request lobRequest) (driver.Result, error) {
	if request.result == nil || request.offset < 0 || request.count < 0 || request.offset > math.MaxInt64-request.count {
		return nil, ErrLOB
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	record, err := c.resourceLocked(request.handle)
	if err != nil {
		return nil, err
	}
	if !record.published || record.lob == nil {
		return nil, ErrResource
	}
	lob := record.lob
	switch request.operation {
	case lobSize:
		request.result.size = int64(lob.locator.ByteLength)
	case lobRead:
		if lob.column.Type != 210 {
			return nil, ErrUnsupported
		}
		if !lob.locator.InRow {
			request.result.n, err = c.readLOBBytesLocked(ctx, lob, request.data, request.offset)
			return result{known: true}, err
		}
		if request.count == 0 {
			break
		}
		if request.offset >= int64(len(lob.locator.Data)) {
			return nil, io.EOF
		}
		request.result.n = copy(request.data, lob.locator.Data[request.offset:])
		if request.result.n < len(request.data) {
			return nil, io.EOF
		}
	case lobReadChars, lobCharLength:
		if lob.column.Type != 211 {
			return nil, ErrUnsupported
		}
		if !lob.locator.InRow {
			if request.operation == lobCharLength {
				request.result.size, err = c.lobCharLengthLocked(ctx, lob)
			} else {
				request.result.text, err = c.readLOBCharsLocked(ctx, lob, request.offset, request.count, c.cfg.Limits.MaxMessageBytes)
			}
			return result{known: true}, err
		}
		text, err := c.decodeLOBText(lob.column, lob.locator.Data)
		if err != nil {
			return nil, err
		}
		if request.operation == lobCharLength {
			request.result.size = int64(utf8.RuneCountInString(text))
			break
		}
		var count int64
		request.result.text, count = lobRuneSlice(text, request.offset, request.count)
		if count < request.count {
			return nil, io.EOF
		}
	case lobWrite, lobTruncate, lobWriteChars, lobTruncateChars:
		err = c.mutateLOBLocked(ctx, lob, request)
		return result{known: true}, err
	default:
		return nil, ErrUnsupported
	}
	return result{known: true}, nil
}

const lobReadBlobSQL = "DECLARE n INTEGER := ?; v RAW(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"

func lobInteger(n int64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, uint64(n))}
}

func (l *nativeLOBResource) parameter() wire.Parameter {
	return wire.Parameter{Type: uint16(l.column.Type), Data: l.raw, LengthEncoded: true}
}

// The caller owns the gate; only a complete response permits this internal command.
func (c *conn) lobCallLocked(ctx context.Context, query string, params []wire.Parameter, count int) ([]driver.Value, error) {
	if c.closed.Load() {
		return nil, ErrResource
	}
	if c.responseActive || !supportsPrepareExecute(c.session.obVersion) {
		return nil, ErrUnsupported
	}
	payload, err := wire.EncodePrepareExecute(0, []byte(query), params, c.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	s := &outputState{internal: true, bindings: make([]*outBinding, count), physical: make([]int, count)}
	for i := range s.physical {
		s.physical[i] = i
	}
	r, err := c.queryPrepareExecuteLocked(ctx, payload, bindPlan{slots: make([]bindSlot, len(params))}, s, nil)
	if err != nil {
		return nil, err
	}
	if err = r.drainAll(ctx); err != nil {
		return nil, err
	}
	return s.values, nil
}

func (c *conn) readLOBBytesLocked(ctx context.Context, lob *nativeLOBResource, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	chunk, err := c.lobChunkSize(lob, lobReadBlobSQL)
	if err != nil {
		return 0, err
	}
	length := int64(lob.locator.ByteLength)
	n := 0
	for n < len(p) && off+int64(n) < length {
		amount := min(int64(len(p)-n), length-off-int64(n), int64(chunk))
		params := []wire.Parameter{lobInteger(amount), lob.parameter(), lobInteger(off + int64(n) + 1), {Type: 6}, {Type: 6}}
		values, err := c.lobCallLocked(ctx, lobReadBlobSQL, params, 2)
		if err != nil {
			return n, err
		}
		got, amountErr := lobAmount(values[0])
		data, binary := values[1].([]byte)
		if amountErr != nil || got < 0 || got > amount || got != int64(len(data)) || got != 0 && !binary {
			return n, ErrLOB
		}
		if got == 0 {
			return n, io.EOF
		}
		n += copy(p[n:], data)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (c *conn) lobChunkSize(lob *nativeLOBResource, query string) (int, error) {
	// Reserve native command fields, length prefixes and amount/offset/output parameters.
	limit := c.cfg.Limits.MaxMessageBytes - int64(len(lob.raw)) - int64(len(query)) - 128
	if limit <= 0 {
		return 0, wire.ErrMessageTooLarge
	}
	return int(min(limit, 32767)), nil
}

func lobAmount(value driver.Value) (int64, error) {
	if n, ok := value.(int64); ok {
		return n, nil
	}
	text, ok := value.(string)
	if !ok {
		return 0, ErrLOB
	}
	plain, err := plainNumber(text, 64)
	if err != nil {
		return 0, ErrLOB
	}
	whole, fraction, _ := strings.Cut(plain, ".")
	if strings.Trim(fraction, "0") != "" {
		return 0, ErrLOB
	}
	return strconv.ParseInt(whole, 10, 64)
}

func (c *conn) decodeLOBText(column wire.Column, data []byte) (string, error) {
	name, err := c.lobCharset(column)
	if err != nil {
		return "", err
	}
	return charset.Decode(name, data)
}

func captureLOBValue(column wire.Column, data []byte) (driver.Value, bool) {
	if column.Type != 210 && column.Type != 211 {
		return nil, false
	}
	if data == nil {
		return nil, true
	}
	return slices.Clone(data), true
}

func (c *conn) registerLOBRowsLocked(session Session, sets []lobRowset) ([]*resourceHandle, error) {
	var handles []*resourceHandle
	for _, set := range sets {
		for _, row := range set.rows {
			for i, column := range set.columns {
				if column.Type != 210 && column.Type != 211 || row[i] == nil {
					continue
				}
				raw, ok := row[i].([]byte)
				if !ok {
					c.discard()
					return nil, ErrLOB
				}
				locator, e := wire.DecodeLOBLocator(raw)
				if e != nil || locator.ByteLength > math.MaxInt64 {
					c.discard()
					return nil, errors.Join(ErrLOB, e)
				}
				h, e := c.registerResourceLocked(session, nil)
				if e != nil {
					c.discard()
					return nil, e
				}
				c.resources.records[h.id].lob = &nativeLOBResource{raw: raw, locator: locator, column: column}
				handles = append(handles, h)
			}
		}
	}
	return handles, nil
}
