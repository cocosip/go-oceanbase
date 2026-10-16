package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"unicode/utf8"
)

// ErrLOB identifies an invalid LOB operation, offset or result shape.
var ErrLOB = errors.New("oceanbase: invalid LOB operation")

// Blob is a binary LOB owned by the caller's pinned SQL session.
type Blob struct{ handle *resourceHandle }

// Clob is a character LOB; public offsets and lengths count Unicode code points.
type Clob struct{ handle *resourceHandle }

// OpenBlob opens exactly one Oracle BLOB locator. SQL NULL returns nil, nil.
func OpenBlob(ctx context.Context, session Session, query string, args ...any) (*Blob, error) {
	h, err := openLOB(ctx, session, query, 210, args)
	if err != nil || h == nil {
		return nil, err
	}
	return &Blob{handle: h}, nil
}

// OpenClob opens exactly one Oracle CLOB/NCLOB locator. SQL NULL returns nil, nil.
func OpenClob(ctx context.Context, session Session, query string, args ...any) (*Clob, error) {
	h, err := openLOB(ctx, session, query, 211, args)
	if err != nil || h == nil {
		return nil, err
	}
	return &Clob{handle: h}, nil
}

type lobOpenRequest struct {
	session Session
	query   string
	args    []driver.NamedValue
	typ     byte
	result  **resourceHandle
}

type lobOperation uint8

const (
	lobRead lobOperation = iota
	lobSize
	lobReadChars
	lobCharLength
	lobWrite
	lobTruncate
	lobWriteChars
	lobTruncateChars
)

type lobRequest struct {
	handle        *resourceHandle
	operation     lobOperation
	offset, count int64
	data          []byte
	text          string
	result        *lobOperationResult
}

type lobOperationResult struct {
	n    int
	size int64
	text string
}

func openLOB(ctx context.Context, session Session, query string, typ byte, args []any) (*resourceHandle, error) {
	if !validSession(session) {
		return nil, ErrResource
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := executionArguments(args)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i].Value, err = convertArgument(values[i].Value)
		if err != nil {
			return nil, err
		}
		if _, output := values[i].Value.(*outBinding); output {
			return nil, ErrLOB
		}
	}
	var h *resourceHandle
	_, err = session.ExecContext(ctx, "", lobOpenRequest{session: session, query: query, args: values, typ: typ, result: &h})
	return h, err
}

func (h *resourceHandle) doLOB(ctx context.Context, request lobRequest) (lobOperationResult, error) {
	var result lobOperationResult
	if h == nil || h.closed.Load() || !validSession(h.session) {
		return result, ErrResource
	}
	if request.offset < 0 || request.count < 0 || request.offset > math.MaxInt64-request.count {
		return result, ErrLOB
	}
	request.handle, request.result = h, &result
	_, err := h.session.ExecContext(ctx, "", request)
	return result, err
}

// ReadAtContext reads bytes using a zero-based offset and io.ReaderAt short-read rules.
func (b *Blob) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if b == nil {
		return 0, ErrResource
	}
	r, err := b.handle.doLOB(ctx, lobRequest{operation: lobRead, offset: off, count: int64(len(p)), data: p})
	return r.n, err
}

// Size returns the BLOB's byte length; table targets query the owning session each time.
func (b *Blob) Size(ctx context.Context) (int64, error) {
	if b == nil {
		return 0, ErrResource
	}
	r, err := b.handle.doLOB(ctx, lobRequest{operation: lobSize})
	return r.size, err
}

// WriteAtContext writes bytes at a zero-based offset, returning a completed prefix on failure.
func (b *Blob) WriteAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if b == nil {
		return 0, ErrResource
	}
	r, err := b.handle.doLOB(ctx, lobRequest{operation: lobWrite, offset: off, count: int64(len(p)), data: p})
	return r.n, err
}

// Truncate shrinks a BLOB to size bytes; it does not extend it.
func (b *Blob) Truncate(ctx context.Context, size int64) error {
	if b == nil {
		return ErrResource
	}
	_, err := b.handle.doLOB(ctx, lobRequest{operation: lobTruncate, count: size})
	return err
}

// Reader returns a sequential reader with its own offset and an explicit operation context.
// Closing the Blob invalidates the reader; this adapter does not own the locator.
func (b *Blob) Reader(ctx context.Context) io.Reader { return &blobStream{blob: b, ctx: ctx} }

// Writer returns a sequential writer starting at byte zero with an explicit operation context.
func (b *Blob) Writer(ctx context.Context) io.Writer { return &blobStream{blob: b, ctx: ctx} }

type blobStream struct {
	blob   *Blob
	ctx    context.Context
	offset int64
}

func (s *blobStream) Read(p []byte) (int, error) {
	n, err := s.blob.ReadAtContext(s.ctx, p, s.offset)
	s.offset += int64(n)
	return n, err
}
func (s *blobStream) Write(p []byte) (int, error) {
	n, err := s.blob.WriteAtContext(s.ctx, p, s.offset)
	s.offset += int64(n)
	return n, err
}

// Close releases the locator without ending the caller's session or transaction.
func (b *Blob) Close() error {
	if b == nil {
		return nil
	}
	return b.handle.close(context.Background())
}

// ReadChars reads up to count Unicode code points from a zero-based offset.
func (c *Clob) ReadChars(ctx context.Context, off, count int64) (string, error) {
	if c == nil {
		return "", ErrResource
	}
	r, err := c.handle.doLOB(ctx, lobRequest{operation: lobReadChars, offset: off, count: count})
	return r.text, err
}

// CharLength returns a Unicode code point count.
func (c *Clob) CharLength(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, ErrResource
	}
	r, err := c.handle.doLOB(ctx, lobRequest{operation: lobCharLength})
	return r.size, err
}

// WriteChars overwrites characters at a zero-based code point offset.
func (c *Clob) WriteChars(ctx context.Context, off int64, value string) (int64, error) {
	if c == nil {
		return 0, ErrResource
	}
	if !utf8.ValidString(value) {
		return 0, ErrType
	}
	r, err := c.handle.doLOB(ctx, lobRequest{operation: lobWriteChars, offset: off, count: int64(utf8.RuneCountInString(value)), text: value})
	return r.size, err
}

// TruncateChars shrinks a character LOB to count Unicode code points.
func (c *Clob) TruncateChars(ctx context.Context, count int64) error {
	if c == nil {
		return ErrResource
	}
	_, err := c.handle.doLOB(ctx, lobRequest{operation: lobTruncateChars, count: count})
	return err
}

// Close releases the locator without ending the caller's session or transaction.
func (c *Clob) Close() error {
	if c == nil {
		return nil
	}
	return c.handle.close(context.Background())
}

func lobRuneSlice(text string, off, count int64) (string, int64) {
	var index int64
	start, end := len(text), len(text)
	for pos := range text {
		if index == off {
			start = pos
		}
		if index == off+count {
			end = pos
			break
		}
		index++
	}
	part := text[start:end]
	return part, int64(utf8.RuneCountInString(part))
}
