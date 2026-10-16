package oceanbase

import (
	"context"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const charsetUTF16 = "utf16"
const typeNameBlob = "BLOB"
const typeNameClob = "CLOB"
const typeNameNClob = "NCLOB"

const lobReadClobSQL = "DECLARE n INTEGER := ?; v VARCHAR2(32767); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"
const lobReadNClobSQL = "DECLARE n INTEGER := ?; v NVARCHAR2(16383); BEGIN BEGIN DBMS_LOB.READ(?, n, ?, v); EXCEPTION WHEN NO_DATA_FOUND THEN n := 0; END; ? := n; ? := v; END;"

func (c *conn) lobCharset(column wire.Column) (string, error) {
	name, ok := charset.Collation(column.Charset)
	if !ok {
		return "", ErrType
	}
	if name == charsetBinary {
		name = c.cfg.Session.Charset
	}
	if name == "" {
		name = charsetUTF8MB4
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

func (c *conn) walkLOBCharsLocked(ctx context.Context, lob *nativeLOBResource, visit func(string, int64) (bool, error)) error {
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
	query := lobReadClobSQL
	if name == charsetUTF16 {
		query = lobReadNClobSQL
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
			return ErrLOB
		}
		if amount == 0 {
			return ErrLOB
		}
		encoded, err := charset.Encode(name, text)
		if err != nil {
			return err
		}
		if uint64(len(encoded)) > lob.locator.ByteLength-bytesRead {
			return ErrLOB
		}
		bytesRead += uint64(len(encoded))
		stop, err := visit(text, offset)
		if err != nil || stop {
			return err
		}
		if amount > math.MaxInt64-offset {
			return ErrLOB
		}
		offset += amount
	}
	if bytesRead != lob.locator.ByteLength {
		return ErrLOB
	}
	return nil
}

func (c *conn) readLOBCharsLocked(ctx context.Context, lob *nativeLOBResource, off, count, limit int64) (string, error) {
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

func (c *conn) lobCharPositionLocked(ctx context.Context, lob *nativeLOBResource, target int64) (int64, error) {
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
		err = ErrLOB
	}
	return server, err
}

func (c *conn) lobCharLengthLocked(ctx context.Context, lob *nativeLOBResource) (int64, error) {
	var length int64
	err := c.walkLOBCharsLocked(ctx, lob, func(text string, _ int64) (bool, error) {
		n := int64(utf8.RuneCountInString(text))
		if length > math.MaxInt64-n {
			return true, ErrLOB
		}
		length += n
		return false, nil
	})
	return length, err
}
