package oceanbase

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const lobWriteSQL = "CALL DBMS_LOB.WRITE(?, ?, ?, ?)"
const lobTrimSQL = "CALL DBMS_LOB.TRIM(?, ?)"
const lobFreeSQL = "CALL DBMS_LOB.FREETEMPORARY(?)"

func (c *conn) mutateLOBLocked(ctx context.Context, lob *nativeLOBResource, request lobRequest) error {
	if lob.locator.ReadOnly {
		return ErrLOB
	}
	characters := request.operation == lobWriteChars || request.operation == lobTruncateChars
	if characters != (lob.column.Type == 211) {
		return ErrLOB
	}
	if request.operation == lobTruncate || request.operation == lobTruncateChars {
		size := request.count
		var err error
		if characters {
			size, err = c.lobCharPositionLocked(ctx, lob, size)
		} else if uint64(size) > lob.locator.ByteLength {
			err = ErrLOB
		}
		if err != nil {
			return err
		}
		return c.updateLOBLocked(ctx, lob, lobTrimSQL, []wire.Parameter{lob.parameter(), lobInteger(size)})
	}
	if characters {
		return c.writeLOBCharsLocked(ctx, lob, request)
	}
	chunk, err := c.lobChunkSize(lob, lobWriteSQL)
	if err != nil {
		return err
	}
	for request.result.n < len(request.data) {
		n := request.result.n
		amount := min(len(request.data)-n, chunk)
		if request.offset+int64(n) == math.MaxInt64 {
			return ErrLOB
		}
		params := []wire.Parameter{lob.parameter(), lobInteger(int64(amount)), lobInteger(request.offset + int64(n) + 1), {Type: 203, Data: request.data[n : n+amount], LengthEncoded: true}}
		if err := c.updateLOBLocked(ctx, lob, lobWriteSQL, params); err != nil {
			return err
		}
		request.result.n += amount
	}
	return nil
}

func (c *conn) updateLOBLocked(ctx context.Context, lob *nativeLOBResource, query string, params []wire.Parameter) error {
	values, err := c.lobCallLocked(ctx, query, params, 1)
	if err != nil {
		return err
	}
	raw, ok := values[0].([]byte)
	if !ok {
		c.discard()
		return ErrLOB
	}
	locator, err := wire.DecodeLOBLocator(raw)
	if err != nil || locator.ByteLength > math.MaxInt64 {
		c.discard()
		return errors.Join(ErrLOB, err)
	}
	lob.raw, lob.locator = raw, locator
	return nil
}

func (c *conn) writeLOBCharsLocked(ctx context.Context, lob *nativeLOBResource, request lobRequest) error {
	name, err := c.lobCharset(lob.column)
	if err != nil {
		return err
	}
	chunkBytes, err := c.lobChunkSize(lob, lobWriteSQL)
	if err != nil {
		return err
	}
	chunk := int64(chunkBytes / 4)
	if chunk == 0 {
		return wire.ErrMessageTooLarge
	}
	// Validate the complete input before changing any server data, without allocating it twice.
	for text := request.text; len(text) > 0; {
		part, _ := lobRuneSlice(text, 0, chunk)
		if _, err = c.lobTextParameter(name, part); err != nil {
			return err
		}
		text = text[len(part):]
	}
	if name == charsetUTF16 && len(request.text) > 0 {
		if err = c.validateLOBUTF16OverwriteLocked(ctx, lob, request.offset, request.text); err != nil {
			return err
		}
	}
	offset, err := c.lobCharPositionLocked(ctx, lob, request.offset)
	if err != nil {
		return err
	}
	text := request.text
	for len(text) > 0 {
		part, n := lobRuneSlice(text, 0, chunk)
		if offset == math.MaxInt64 {
			return ErrLOB
		}
		parameter, err := c.lobTextParameter(name, part)
		if err != nil {
			return err
		}
		amount := lobUnits(part, name == charsetUTF16)
		if offset > math.MaxInt64-amount {
			return ErrLOB
		}
		params := []wire.Parameter{lob.parameter(), lobInteger(amount), lobInteger(offset + 1), parameter}
		if err = c.updateLOBLocked(ctx, lob, lobWriteSQL, params); err != nil {
			return err
		}
		request.result.size += n
		offset += amount
		text = text[len(part):]
	}
	return nil
}

func (c *conn) validateLOBUTF16OverwriteLocked(ctx context.Context, lob *nativeLOBResource, off int64, value string) error {
	var position int64
	return c.walkLOBCharsLocked(ctx, lob, func(text string, _ int64) (bool, error) {
		for _, old := range text {
			if position >= off {
				if len(value) == 0 {
					return true, nil
				}
				current, n := utf8.DecodeRuneInString(value)
				// Different UTF-16 widths would replace a different span or split an existing surrogate pair.
				if (old > 65535) != (current > 65535) {
					return true, ErrUnsupported
				}
				value = value[n:]
			}
			position++
		}
		return len(value) == 0, nil
	})
}

func (c *conn) lobTextParameter(name, text string) (wire.Parameter, error) {
	if !utf8.ValidString(text) {
		return wire.Parameter{}, ErrType
	}
	if _, err := charset.Encode(name, text); err != nil {
		return wire.Parameter{}, err
	}
	var value any = text
	if name == charsetUTF16 {
		value = NString(text)
	}
	return encodeParameter(value, ModeOracle, c.cfg.Session, 32767)
}

func (c *conn) freeLOBLocked(ctx context.Context, lob *nativeLOBResource) error {
	if c.closed.Load() || lob.locator.Version == 2 && lob.locator.Kind == 0 {
		return nil
	}
	query := lobFreeSQL
	params := []wire.Parameter{lob.parameter()}
	if lob.locator.Version == 1 {
		typ := typeNameBlob
		if lob.column.Type == 211 {
			typ = typeNameClob
		}
		query = "DECLARE l " + typ + " := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;"
		params = append(params, wire.Parameter{Type: 6})
	}
	_, err := c.lobCallLocked(ctx, query, params, 1)
	if err != nil {
		c.discard()
	}
	return err
}
