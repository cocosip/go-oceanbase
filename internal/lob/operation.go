// Package lob owns locator and table LOB state and chunked I/O.
package lob

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Operation selects a binary or character LOB action.
type Operation uint8

// Supported LOB operation kinds.
const (
	Read Operation = iota
	Size
	ReadChars
	CharLength
	Write
	Truncate
	WriteChars
	TruncateChars
)

// Request carries validated LOB operation arguments inside this owner.
type Request struct {
	operation     Operation
	offset, count int64
	data          []byte
	text          string
	result        *Result
}

// Result contains a LOB action's byte count, length, or text.
type Result struct {
	n    int
	size int64
	text string
}

type nativeOperation struct{ op *session.Operation }

// AdoptLocator validates the complete locator before it becomes an owned resource.
func AdoptLocator(column wire.Column, raw []byte) (*Native, error) {
	n, err := DecodeLocator(column, raw)
	if err != nil || n.locator.ByteLength > math.MaxInt64 {
		return nil, errors.Join(dberrors.ErrLOB, err)
	}
	return n, nil
}

// DecodeLocator keeps the wire error identity for buffered scalar materialization.
func DecodeLocator(column wire.Column, raw []byte) (*Native, error) {
	locator, err := wire.DecodeLOBLocator(raw)
	if err != nil {
		return nil, err
	}
	return &Native{raw: raw, locator: locator, column: column}, nil
}

// Column returns the locator's original wire column.
func (n *Native) Column() wire.Column { return n.column }

// ByteLength returns the locator's encoded byte length.
func (n *Native) ByteLength() int64 { return int64(n.locator.ByteLength) }

// InRow reports whether the locator contains inline data.
func (n *Native) InRow() bool { return n.locator.InRow }

// InRowData returns inline bytes owned by the locator.
func (n *Native) InRowData() []byte { return n.locator.Data }

func (c *nativeOperation) lobCallLocked(ctx context.Context, query string, params []wire.Parameter, count int) ([]driver.Value, error) {
	key := c.op.Epoch()
	s, err := statement.NativeCallRaw(ctx, c.op, query, params, count)
	if err != nil {
		return nil, err
	}
	if key != c.op.Epoch() {
		return nil, dberrors.ErrResource
	}
	return s.Values, nil
}

// ReadBytes reads a bounded binary locator range.
func ReadBytes(ctx context.Context, op *session.Operation, n *Native, p []byte, off int64) (int, error) {
	return (&nativeOperation{op}).readLOBBytesLocked(ctx, n, p, off)
}

// ReadText reads a bounded character locator range.
func ReadText(ctx context.Context, op *session.Operation, n *Native, off, count, limit int64) (string, error) {
	return (&nativeOperation{op}).readLOBCharsLocked(ctx, n, off, count, limit)
}

// DecodeText applies the locator column's character encoding.
func DecodeText(op *session.Operation, column wire.Column, data []byte) (string, error) {
	return (&nativeOperation{op}).decodeLOBText(column, data)
}

// Free releases a temporary native locator when required.
func Free(ctx context.Context, op *session.Operation, n *Native) error {
	return (&nativeOperation{op}).freeLOBLocked(ctx, n)
}

// ExecuteNative runs an already validated handle operation under the caller's lease.
func ExecuteNative(ctx context.Context, op *session.Operation, n *Native, operation Operation, offset, count int64, data []byte, text string) (Result, error) {
	var result Result
	if offset < 0 || count < 0 || offset > math.MaxInt64-count {
		return result, dberrors.ErrLOB
	}
	c := &nativeOperation{op}
	r := Request{operation, offset, count, data, text, &result}
	var err error
	switch operation {
	case Size:
		result.size = int64(n.locator.ByteLength)
	case Read:
		if n.column.Type != 210 {
			return result, dberrors.ErrUnsupported
		}
		if !n.locator.InRow {
			result.n, err = c.readLOBBytesLocked(ctx, n, data, offset)
			return result, err
		}
		if count == 0 {
			break
		}
		if offset >= int64(len(n.locator.Data)) {
			return result, io.EOF
		}
		result.n = copy(data, n.locator.Data[offset:])
		if result.n < len(data) {
			err = io.EOF
		}
	case ReadChars, CharLength:
		if n.column.Type != 211 {
			return result, dberrors.ErrUnsupported
		}
		if !n.locator.InRow {
			if operation == CharLength {
				result.size, err = c.lobCharLengthLocked(ctx, n)
			} else {
				result.text, err = c.readLOBCharsLocked(ctx, n, offset, count, op.Config().Limits.MaxMessageBytes)
			}
			return result, err
		}
		value, e := c.decodeLOBText(n.column, n.locator.Data)
		if e != nil {
			return result, e
		}
		if operation == CharLength {
			result.size = int64(utf8.RuneCountInString(value))
			break
		}
		var got int64
		result.text, got = lobRuneSlice(value, offset, count)
		if got < count {
			err = io.EOF
		}
	case Write, Truncate, WriteChars, TruncateChars:
		err = c.mutateLOBLocked(ctx, n, r)
	default:
		err = dberrors.ErrUnsupported
	}
	return result, err
}

// Count returns the number of bytes processed.
func (r Result) Count() int { return r.n }

// Size returns the resulting byte or character length.
func (r Result) Size() int64 { return r.size }

// Text returns the character data read.
func (r Result) Text() string { return r.text }
