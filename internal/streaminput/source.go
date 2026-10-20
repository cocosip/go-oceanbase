// Package streaminput owns bounded input reading and one-use input state.
package streaminput

import (
	"context"
	"io"
	"reflect"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Source carries the original reader and a reference to its one-use state.
// Copies share State; validation and planning never read or claim the source.
type Source struct {
	Reader   io.Reader
	Length   int64
	MaxBytes int64
	Text     bool
	State    *atomic.Bool
}

// Validate rejects invalid or consumed input without reading or claiming it.
func (s *Source) Validate() error {
	if s == nil || NilReader(s.Reader) || s.Length < -1 || s.MaxBytes < 0 || s.State == nil || s.State.Load() {
		return dberrors.ErrLongData
	}
	return nil
}

// Claim is called only at the original upload boundary, after prevalidation.
func (s *Source) Claim() bool {
	return s != nil && s.State != nil && s.State.CompareAndSwap(false, true)
}

// NilReader detects nil and typed-nil reader implementations.
func NilReader(reader io.Reader) bool {
	if reader == nil {
		return true
	}
	v := reflect.ValueOf(reader)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// BoundedReader checks cancellation, exact length and byte limits around reads.
type BoundedReader struct {
	ctx                 context.Context
	reader              io.Reader
	length, limit, read int64
	idle                int
}

// NewBoundedReader creates a reader with cancellation, source-length and byte limits.
func NewBoundedReader(ctx context.Context, reader io.Reader, length, limit int64) *BoundedReader {
	return &BoundedReader{ctx: ctx, reader: reader, length: length, limit: limit}
}

// ReadCount reports accepted source bytes.
func (s *BoundedReader) ReadCount() int64 { return s.read }

func (s *BoundedReader) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	remaining := s.limit - s.read
	if s.length >= 0 {
		remaining = min(remaining, s.length-s.read)
	}
	// Probe one byte past an exact bound without overflowing MaxInt64.
	if int64(len(p)) > remaining {
		p = p[:int(remaining)+1]
	}
	n, err := s.reader.Read(p)
	if n < 0 || n > len(p) {
		return 0, dberrors.ErrLongData
	}
	if cause := s.ctx.Err(); cause != nil {
		return 0, cause
	}
	if int64(n) > s.limit-s.read {
		return 0, wire.ErrMessageTooLarge
	}
	if s.length >= 0 && int64(n) > s.length-s.read {
		return 0, dberrors.ErrLongData
	}
	s.read += int64(n)
	if err == io.EOF && s.length >= 0 && s.read < s.length {
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

// ReadChunk fills a chunk or returns the first source error.
func ReadChunk(r io.Reader, p []byte) (int, error) {
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
