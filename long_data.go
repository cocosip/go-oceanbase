package oceanbase

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// LongData is a one-use streamed IN parameter. Pass a pointer; do not copy it after use.
// Reader remains caller-owned, including on failure. It must return for cancellation to finish.
// Length is the exact source byte count; -1 reads to EOF. Zero requires an empty Reader.
// Text reads UTF-8 and converts to Session.Charset; otherwise bytes are binary.
// MaxBytes bounds source and converted bytes separately; zero uses Limits.MaxLOBBytes.
// Fields and Reader must not be mutated concurrently with execution.
type LongData struct {
	Reader   io.Reader
	Length   int64
	MaxBytes int64
	Text     bool
	used     atomic.Bool
}

// ErrLongData identifies invalid, consumed or failed streamed parameters.
var ErrLongData = errors.New("oceanbase: invalid or failed long data")

func (v *LongData) validate() error {
	if v == nil || v.Reader == nil || v.Length < -1 || v.MaxBytes < 0 || v.used.Load() {
		return ErrLongData
	}
	if r := reflect.ValueOf(v.Reader); r.Kind() == reflect.Pointer && r.IsNil() {
		return ErrLongData
	}
	return nil
}

func (c *conn) longDataParameter(v *LongData) (wire.Parameter, error) {
	if err := v.validate(); err != nil {
		return wire.Parameter{}, err
	}
	limit := v.MaxBytes
	if limit == 0 {
		limit = c.cfg.Limits.MaxLOBBytes
	}
	if v.Length > limit {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	typ := uint16(252)
	if v.Text {
		typ = 253
	}
	if c.session.mode == ModeOracle {
		if !supportsPrepareExecute(c.session.obVersion) {
			return wire.Parameter{}, ErrUnsupported
		}
		typ = 254 // Java OBStreamParameter/OBReaderParameter send STRING, with binary hex pieces.
	}
	if c.cfg.Limits.MaxMessageBytes < 25 || v.Text && c.longChunkSize(v) < utf8.UTFMax {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	return wire.Parameter{Type: typ, LongData: true}, nil
}

func longValues(values []boundValue) bool {
	for _, v := range values {
		if _, ok := v.value.(*LongData); ok {
			return true
		}
	}
	return false
}

// Upload owns the same gate and transaction as execution; failed uploads discard server state.
func (c *conn) uploadLongDataLocked(ctx context.Context, id uint32, values []boundValue) (err error) {
	if !longValues(values) {
		return nil
	}
	groups := make(map[*LongData][]uint16)
	var order []*LongData
	for i, value := range values {
		if v, ok := value.value.(*LongData); ok {
			if _, err = c.longDataParameter(v); err != nil {
				return err
			}
			if groups[v] == nil {
				order = append(order, v)
			}
			groups[v] = append(groups[v], uint16(i))
		}
	}
	r := c.startRows(ctx, true, nil)
	r.keepGate = true
	defer func() {
		if err != nil {
			c.discard()
		}
		r.err = err
		r.release()
		err = r.err
		// database/sql retries ErrBadConn; a Reader must never induce an automatic replay.
		if errors.Is(err, driver.ErrBadConn) {
			err = fmt.Errorf("%w: %v", ErrLongData, err)
		} else if err != nil {
			err = errors.Join(ErrLongData, err)
		}
	}()
	for _, v := range order {
		if !v.used.CompareAndSwap(false, true) {
			return ErrLongData
		}
		if err = c.uploadLongValueLocked(r.ctx, id, groups[v], v); err != nil {
			return err
		}
	}
	return nil
}

type longSource struct {
	ctx         context.Context
	value       *LongData
	limit, read int64
	idle        int
}

func readLongChunk(r io.Reader, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		m, err := r.Read(p[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *longSource) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	remaining := s.limit - s.read
	if s.value.Length >= 0 {
		remaining = min(remaining, s.value.Length-s.read)
	}
	// Probe one byte past an exact bound without overflowing MaxInt64.
	if int64(len(p)) > remaining {
		p = p[:int(remaining)+1]
	}
	n, err := s.value.Reader.Read(p)
	if n < 0 || n > len(p) {
		return 0, ErrLongData
	}
	if cause := s.ctx.Err(); cause != nil {
		return 0, cause
	}
	if int64(n) > s.limit-s.read {
		return 0, wire.ErrMessageTooLarge
	}
	if s.value.Length >= 0 && int64(n) > s.value.Length-s.read {
		return 0, ErrLongData
	}
	s.read += int64(n)
	if err == io.EOF && s.value.Length >= 0 && s.read < s.value.Length {
		return 0, io.ErrUnexpectedEOF
	}
	if n == 0 && err == nil {
		s.idle++
		if s.idle >= 100 {
			return 0, io.ErrNoProgress
		}
	} else {
		s.idle = 0
	}
	return n, err
}

func (c *conn) uploadLongValueLocked(ctx context.Context, id uint32, indices []uint16, v *LongData) error {
	limit := v.MaxBytes
	if limit == 0 {
		limit = c.cfg.Limits.MaxLOBBytes
	}
	source := &longSource{ctx: ctx, value: v, limit: limit}
	size := c.longChunkSize(v)
	buf := make([]byte, int(size)+utf8.UTFMax)
	pending := 0
	first := true
	var encoded int64
	for {
		n, err := readLongChunk(source, buf[pending:int(size)])
		n += pending
		final := err == io.EOF
		if err != nil && !final {
			return err
		}
		if final && v.Length >= 0 && source.read != v.Length {
			return io.ErrUnexpectedEOF
		}
		data := buf[:n]
		end := n
		if v.Text {
			var e error
			end, e = longTextBoundary(data, final)
			if e != nil {
				return e
			}
			name := c.cfg.Session.Charset
			if name == "" {
				name = charsetUTF8MB4
			}
			data, e = charset.Encode(name, string(data[:end]))
			if e != nil {
				return e
			}
		}
		if int64(len(data)) > limit-encoded {
			return wire.ErrMessageTooLarge
		}
		encoded += int64(len(data))
		if c.session.mode == ModeOracle && !v.Text {
			data = []byte(hex.EncodeToString(data))
		}
		piece := byte(2)
		if first {
			piece = 1
		}
		if final {
			piece = 3
		}
		for _, index := range indices {
			if err = c.sendLongPieceLocked(ctx, id, index, piece, data); err != nil {
				return err
			}
		}
		if final {
			return nil
		}
		pending = copy(buf, buf[end:n])
		first = false
	}
}

func (c *conn) longChunkSize(v *LongData) int64 {
	size := min(int64(65536), c.cfg.Limits.MaxMessageBytes-7)
	if c.session.mode == ModeOracle {
		size = (min(int64(65536), c.cfg.Limits.MaxMessageBytes) - 17) / 2
	}
	if v.Text {
		size /= 4
	}
	return size
}

func longTextBoundary(data []byte, final bool) (int, error) {
	for i := 0; i < len(data); {
		r, n := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && n == 1 {
			if !final && !utf8.FullRune(data[i:]) {
				return i, nil
			}
			return 0, charset.ErrEncoding
		}
		i += n
	}
	return len(data), nil
}

func (c *conn) sendLongPieceLocked(ctx context.Context, id uint32, index uint16, piece byte, data []byte) error {
	b := binary.LittleEndian.AppendUint32([]byte{24}, id)
	b = binary.LittleEndian.AppendUint16(b, index)
	if c.session.mode == ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	b = append(b, data...)
	p := c.session.packets
	p.ResetSequence()
	if err := p.WriteMessage(ctx, b); err != nil {
		return err
	}
	if c.session.mode != ModeOracle {
		return nil
	}
	response, err := p.ReadMessage(ctx)
	if err != nil {
		return err
	}
	if len(response) > 0 && response[0] == 255 {
		return decodeServerError(response)
	}
	ok, err := wire.DecodeOK(response, c.session.capabilities)
	if err != nil {
		return err
	}
	if ok.Status&8 != 0 {
		return wire.ErrMalformed
	}
	c.observeResourceStatus(ok.Status)
	c.session.ok = ok
	return nil
}
