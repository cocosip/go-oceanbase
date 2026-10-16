package oceanbase

import (
	"database/sql/driver"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// UnsupportedTypeError identifies a type whose decoding is not implemented for this mode.
type UnsupportedTypeError struct {
	Type byte
	Mode Mode
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("oceanbase: unsupported type %d in compatibility mode %d", e.Type, e.Mode)
}
func (*UnsupportedTypeError) Unwrap() error { return ErrUnsupported }

func textTypeName(c wire.Column, mode Mode) string {
	if mode == ModeOracle {
		switch c.Type {
		case 3:
			return "NUMBER"
		case 4:
			return "BINARY_FLOAT"
		case 5:
			return "BINARY_DOUBLE"
		case 15, 253:
			return "VARCHAR2"
		case 203:
			return "RAW"
		case 7, 10, 11, 13, 14, 245, 249, 250, 251, 252, 255:
			return ""
		}
	}
	return mysqlTextTypeName(c)
}

func mysqlTextTypeName(c wire.Column) string {
	switch c.Type {
	case 0, 246:
		return "DECIMAL"
	case 1:
		return "TINYINT"
	case 2:
		return "SMALLINT"
	case 3:
		return "INTEGER"
	case 4:
		return "FLOAT"
	case 5:
		return "DOUBLE"
	case 6:
		return "NULL"
	case 7:
		return "TIMESTAMP"
	case 8:
		return "BIGINT"
	case 9:
		return "MEDIUMINT"
	case 10, 14:
		return "DATE"
	case 11:
		return "TIME"
	case 12:
		return "DATETIME"
	case 13:
		return "YEAR"
	case 15, 253:
		return "VARCHAR"
	case 16:
		return "BIT"
	case 245:
		return "JSON"
	case 247:
		return "ENUM"
	case 248:
		return "SET"
	case 249, 250, 251, 252:
		if c.Charset == 63 {
			return "BLOB"
		}
		return "TEXT"
	case 254:
		return "CHAR"
	case 255:
		return "GEOMETRY"
	}
	return ""
}

func decodeTextValue(c wire.Column, mode Mode, data []byte) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	if textTypeName(c, mode) == "" {
		return nil, &UnsupportedTypeError{Type: c.Type, Mode: mode}
	}
	switch c.Type {
	case 0, 246:
		return string(data), nil
	case 1, 2, 3, 8, 9, 13:
		if mode == ModeOracle && c.Type == 3 {
			return string(data), nil
		}
		if c.Flags&32 != 0 {
			n, err := strconv.ParseUint(string(data), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("oceanbase: invalid unsigned integer")
			}
			if n > math.MaxInt64 {
				return string(data), nil
			}
			return int64(n), nil
		}
		n, err := strconv.ParseInt(string(data), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("oceanbase: invalid signed integer")
		}
		return n, nil
	case 4, 5:
		bits := 64
		if c.Type == 4 {
			bits = 32
		}
		n, err := strconv.ParseFloat(string(data), bits)
		if err != nil || (mode != ModeOracle && (math.IsNaN(n) || math.IsInf(n, 0))) {
			return nil, fmt.Errorf("oceanbase: invalid floating point value")
		}
		return n, nil
	case 6:
		return nil, nil
	case 7, 10, 11, 12, 14:
		return string(data), nil
	case 16, 203, 255:
		return slices.Clone(data), nil
	case 245:
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("oceanbase: invalid UTF-8 JSON")
		}
		return string(data), nil
	}
	if c.Charset == 63 {
		return slices.Clone(data), nil
	}
	if !utf8Collation(c.Charset) {
		return nil, fmt.Errorf("%w: result character set", ErrUnsupported)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("oceanbase: invalid UTF-8 result")
	}
	return string(data), nil
}

func utf8Collation(id uint16) bool {
	return id == 33 || id == 45 || id == 46 || id == 83 || (id >= 192 && id <= 215) || (id >= 224 && id <= 247) || id == 255
}
