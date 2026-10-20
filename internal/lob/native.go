package lob

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

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

const charsetUTF16 = "utf16"

// ReadClobSQL reads database character data using DBMS_LOB.
const ReadClobSQL = "DECLARE n INTEGER := ?; v VARCHAR2(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"

// ReadNClobSQL reads national character data using DBMS_LOB.
const ReadNClobSQL = "DECLARE n INTEGER := ?; v NVARCHAR2(16383); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"

func (c *nativeOperation) lobCharset(column wire.Column) (string, error) {
	name, ok := charset.Collation(column.Charset)
	if !ok {
		return "", dberrors.ErrType
	}
	if name == "binary" {
		name = c.op.Config().Session.Charset
	}
	if name == "" {
		name = "utf8mb4"
	}
	return name, nil
}

// UTF-16 uses code units on the server; the public API always counts code points.
func lobUnits(text string, utf16 bool) int64 {
	var count int64
	for _, r := range text {
		count++
		if utf16 && r > 65535 {
			count++
		}
	}
	return count
}

func (c *nativeOperation) walkLOBCharsLocked(ctx context.Context, lob *Native, visit func(string, int64) (bool, error)) error {
	name, err := c.lobCharset(lob.column)
	if err != nil {
		return err
	}
	if lob.locator.InRow {
		text, err := c.decodeLOBText(lob.column, lob.locator.Data)
		if err != nil {
			return err
		}
		_, err = visit(text, 0)
		return err
	}
	query := ReadClobSQL
	if name == charsetUTF16 {
		query = ReadNClobSQL
	}
	chunkBytes, err := c.lobChunkSize(lob, query)
	if err != nil {
		return err
	}
	chunk := int64(chunkBytes / 4)
	if chunk == 0 {
		return wire.ErrMessageTooLarge
	}
	var offset int64
	var bytesRead uint64
	for bytesRead < lob.locator.ByteLength && offset < math.MaxInt64 {
		params := []wire.Parameter{lobInteger(chunk), lob.parameter(), lobInteger(offset + 1), {Type: 6}, {Type: 6}}
		values, err := c.lobCallLocked(ctx, query, params, 2)
		if err != nil {
			return err
		}
		amount, amountErr := lobAmount(values[0])
		text, textOK := values[1].(string)
		if amount == 0 && values[1] == nil {
			textOK = true
		}
		if amountErr != nil || !textOK || amount < 0 || amount > chunk || amount != lobUnits(text, name == charsetUTF16) {
			return dberrors.ErrLOB
		}
		if amount == 0 {
			return dberrors.ErrLOB
		}
		encoded, err := charset.Encode(name, text)
		if err != nil {
			return err
		}
		if uint64(len(encoded)) > lob.locator.ByteLength-bytesRead {
			return dberrors.ErrLOB
		}
		bytesRead += uint64(len(encoded))
		stop, err := visit(text, offset)
		if err != nil || stop {
			return err
		}
		if amount > math.MaxInt64-offset {
			return dberrors.ErrLOB
		}
		offset += amount
	}
	if bytesRead != lob.locator.ByteLength {
		return dberrors.ErrLOB
	}
	return nil
}

func (c *nativeOperation) readLOBCharsLocked(ctx context.Context, lob *Native, off, count, limit int64) (string, error) {
	if count == 0 {
		return "", nil
	}
	var position, read int64
	var value strings.Builder
	err := c.walkLOBCharsLocked(ctx, lob, func(text string, _ int64) (bool, error) {
		length := int64(utf8.RuneCountInString(text))
		if position+length > off {
			part, n := lobRuneSlice(text, max(0, off-position), count-read)
			if int64(value.Len()) > limit-int64(len(part)) {
				return true, wire.ErrMessageTooLarge
			}
			value.WriteString(part)
			read += n
		}
		position += length
		return read == count, nil
	})
	if err == nil && read < count {
		err = io.EOF
	}
	return value.String(), err
}

func (c *nativeOperation) lobCharPositionLocked(ctx context.Context, lob *Native, target int64) (int64, error) {
	if target == 0 {
		return 0, nil
	}
	name, err := c.lobCharset(lob.column)
	if err != nil {
		return 0, err
	}
	var position, server int64
	err = c.walkLOBCharsLocked(ctx, lob, func(text string, offset int64) (bool, error) {
		length := int64(utf8.RuneCountInString(text))
		if target <= position+length {
			prefix, _ := lobRuneSlice(text, 0, target-position)
			server = offset + lobUnits(prefix, name == charsetUTF16)
			position = target
			return true, nil
		}
		position += length
		return false, nil
	})
	if err == nil && position != target {
		err = dberrors.ErrLOB
	}
	return server, err
}

func (c *nativeOperation) lobCharLengthLocked(ctx context.Context, lob *Native) (int64, error) {
	var length int64
	err := c.walkLOBCharsLocked(ctx, lob, func(text string, _ int64) (bool, error) {
		n := int64(utf8.RuneCountInString(text))
		if length > math.MaxInt64-n {
			return true, dberrors.ErrLOB
		}
		length += n
		return false, nil
	})
	return length, err
}

// WriteSQL writes a native locator using DBMS_LOB.
const WriteSQL = "CALL DBMS_LOB.WRITE(?, ?, ?, ?)"

const lobTrimSQL = "CALL DBMS_LOB.TRIM(?, ?)"

const lobFreeSQL = "DECLARE l BLOB := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;"

func (c *nativeOperation) mutateLOBLocked(ctx context.Context, lob *Native, request Request) error {
	if lob.locator.ReadOnly {
		return dberrors.ErrLOB
	}
	characters := request.operation == WriteChars || request.operation == TruncateChars
	if characters != (lob.column.Type == 211) {
		return dberrors.ErrLOB
	}
	if request.operation == Truncate || request.operation == TruncateChars {
		size := request.count
		var err error
		if characters {
			size, err = c.lobCharPositionLocked(ctx, lob, size)
		} else if uint64(size) > lob.locator.ByteLength {
			err = dberrors.ErrLOB
		}
		if err != nil {
			return err
		}
		return c.updateLOBLocked(ctx, lob, lobTrimSQL, []wire.Parameter{lob.parameter(), lobInteger(size)})
	}
	if characters {
		return c.writeLOBCharsLocked(ctx, lob, request)
	}
	chunk, err := c.lobChunkSize(lob, WriteSQL)
	if err != nil {
		return err
	}
	for request.result.n < len(request.data) {
		n := request.result.n
		amount := min(len(request.data)-n, chunk)
		if request.offset+int64(n) == math.MaxInt64 {
			return dberrors.ErrLOB
		}
		params := []wire.Parameter{lob.parameter(), lobInteger(int64(amount)), lobInteger(request.offset + int64(n) + 1), {Type: 203, Data: request.data[n : n+amount], LengthEncoded: true}}
		if err := c.updateLOBLocked(ctx, lob, WriteSQL, params); err != nil {
			return err
		}
		request.result.n += amount
	}
	return nil
}

func (c *nativeOperation) updateLOBLocked(ctx context.Context, lob *Native, query string, params []wire.Parameter) error {
	values, err := c.lobCallLocked(ctx, query, params, 1)
	if err != nil {
		return err
	}
	raw, ok := values[0].([]byte)
	if !ok {
		c.op.Discard()
		return dberrors.ErrLOB
	}
	locator, err := wire.DecodeLOBLocator(raw)
	if err != nil || locator.ByteLength > math.MaxInt64 {
		c.op.Discard()
		return errors.Join(dberrors.ErrLOB, err)
	}
	lob.raw, lob.locator = raw, locator
	return nil
}

func (c *nativeOperation) writeLOBCharsLocked(ctx context.Context, lob *Native, request Request) error {
	name, err := c.lobCharset(lob.column)
	if err != nil {
		return err
	}
	chunkBytes, err := c.lobChunkSize(lob, WriteSQL)
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
			return dberrors.ErrLOB
		}
		parameter, err := c.lobTextParameter(name, part)
		if err != nil {
			return err
		}
		amount := lobUnits(part, name == charsetUTF16)
		if offset > math.MaxInt64-amount {
			return dberrors.ErrLOB
		}
		params := []wire.Parameter{lob.parameter(), lobInteger(amount), lobInteger(offset + 1), parameter}
		if err = c.updateLOBLocked(ctx, lob, WriteSQL, params); err != nil {
			return err
		}
		request.result.size += n
		offset += amount
		text = text[len(part):]
	}
	return nil
}

func (c *nativeOperation) validateLOBUTF16OverwriteLocked(ctx context.Context, lob *Native, off int64, value string) error {
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
					return true, dberrors.ErrUnsupported
				}
				value = value[n:]
			}
			position++
		}
		return len(value) == 0, nil
	})
}

func (c *nativeOperation) lobTextParameter(name, text string) (wire.Parameter, error) {
	if !utf8.ValidString(text) {
		return wire.Parameter{}, dberrors.ErrType
	}
	if _, err := charset.Encode(name, text); err != nil {
		return wire.Parameter{}, err
	}
	var value any = text
	if name == charsetUTF16 {
		value = types.NString(text)
	}
	return valuecodec.EncodeParameter(value, config.ModeOracle, c.op.Config().Session, 32767)
}

func (c *nativeOperation) freeLOBLocked(ctx context.Context, lob *Native) error {
	if c.op.Closed() || lob.locator.Version == 2 && lob.locator.Kind == 0 {
		return nil
	}
	query := lobFreeSQL
	params := []wire.Parameter{lob.parameter(), {Type: 6}}
	if lob.column.Type == 211 {
		query = "DECLARE l CLOB := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;"
	}
	_, err := c.lobCallLocked(ctx, query, params, 1)
	if err != nil {
		c.op.Discard()
	}
	return err
}

// Native retains the decoded locator and its source column.
type Native struct {
	raw     []byte
	locator wire.LOBLocator
	column  wire.Column
}

// ReadBlobSQL reads binary locator data using DBMS_LOB.
const ReadBlobSQL = "DECLARE n INTEGER := ?; v RAW(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"

func lobInteger(n int64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, uint64(n))}
}

func (l *Native) parameter() wire.Parameter {
	return wire.Parameter{Type: uint16(l.column.Type), Data: l.raw, LengthEncoded: true}
}

func (c *nativeOperation) readLOBBytesLocked(ctx context.Context, lob *Native, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	chunk, err := c.lobChunkSize(lob, ReadBlobSQL)
	if err != nil {
		return 0, err
	}
	length := int64(lob.locator.ByteLength)
	n := 0
	for n < len(p) && off+int64(n) < length {
		amount := min(int64(len(p)-n), length-off-int64(n), int64(chunk))
		params := []wire.Parameter{lobInteger(amount), lob.parameter(), lobInteger(off + int64(n) + 1), {Type: 6}, {Type: 6}}
		values, err := c.lobCallLocked(ctx, ReadBlobSQL, params, 2)
		if err != nil {
			return n, err
		}
		got, amountErr := lobAmount(values[0])
		data, binary := values[1].([]byte)
		if amountErr != nil || got < 0 || got > amount || got != int64(len(data)) || got != 0 && !binary {
			return n, dberrors.ErrLOB
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

func (c *nativeOperation) lobChunkSize(lob *Native, query string) (int, error) {
	// Reserve native command fields, length prefixes and amount/offset/output parameters.
	limit := c.op.Config().Limits.MaxMessageBytes - int64(len(lob.raw)) - int64(len(query)) - 128
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
		return 0, dberrors.ErrLOB
	}
	plain, err := valueformat.PlainNumber(text, 64)
	if err != nil {
		return 0, dberrors.ErrLOB
	}
	whole, fraction, _ := strings.Cut(plain, ".")
	if strings.Trim(fraction, "0") != "" {
		return 0, dberrors.ErrLOB
	}
	return strconv.ParseInt(whole, 10, 64)
}

func (c *nativeOperation) decodeLOBText(column wire.Column, data []byte) (string, error) {
	name, err := c.lobCharset(column)
	if err != nil {
		return "", err
	}
	return charset.Decode(name, data)
}
