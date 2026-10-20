package valuecodec

import (
	"database/sql/driver"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// TextTypeName maps wire metadata to the compatibility mode type name.
func TextTypeName(c wire.Column, mode config.Mode) string {
	if mode == config.ModeOracle {
		switch c.Type {
		case 163:
			return valueformat.TypeNameRefCursor
		case 160:
			return "OBJECT/ARRAY"
		case 3:
			return valueformat.TypeNameNumber
		case 4:
			return valueformat.TypeNameBinaryFloat
		case 5:
			return valueformat.TypeNameBinaryDouble
		case 12:
			return valueformat.TypeNameDate
		case 15, 253:
			return valueformat.TypeNameVarchar2
		case 254:
			return valueformat.TypeNameChar
		case 203:
			return valueformat.TypeNameRaw
		case 200:
			return valueformat.TypeNameTimestampTZ
		case 201:
			return valueformat.TypeNameTimestampLTZ
		case 202:
			return valueformat.TypeNameTimestamp
		case 204:
			return valueformat.TypeNameIntervalYM
		case 205:
			return valueformat.TypeNameIntervalDS
		case 206:
			return valueformat.TypeNameNumber
		case 207:
			return valueformat.TypeNameNVarchar2
		case 208:
			return valueformat.TypeNameNChar
		case 209:
			return valueformat.TypeNameRowID
		case 210:
			return valueformat.TypeNameBlob
		case 211:
			if name, _ := charset.Collation(c.Charset); name == charsetUTF16 {
				return valueformat.TypeNameNClob
			}
			return valueformat.TypeNameClob
		case 246:
			return valueformat.TypeNameNumber
		case 7, 10, 11, 13, 14, 245, 249, 250, 251, 252, 255:
			return ""
		}
	}
	return mysqlTextTypeName(c)
}

func mysqlTextTypeName(c wire.Column) string {
	switch c.Type {
	case 0, 246:
		return valueformat.TypeNameDecimal
	case 1:
		return "TINYINT"
	case 2:
		return "SMALLINT"
	case 3:
		return valueformat.TypeNameInteger
	case 4:
		return valueformat.TypeNameFloat
	case 5:
		return valueformat.TypeNameDouble
	case 6:
		return nullTypeName
	case 7:
		return valueformat.TypeNameTimestamp
	case 8:
		return valueformat.TypeNameBigInt
	case 9:
		return "MEDIUMINT"
	case 10, 14:
		return valueformat.TypeNameDate
	case 11:
		return valueformat.TypeNameTime
	case 12:
		return valueformat.TypeNameDateTime
	case 13:
		return "YEAR"
	case 15, 253:
		if c.Charset == 63 {
			return valueformat.TypeNameVarbinary
		}
		return valueformat.TypeNameVarchar
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
			return valueformat.TypeNameBlob
		}
		return "TEXT"
	case 254:
		if c.Charset == 63 {
			return valueformat.TypeNameBinary
		}
		return valueformat.TypeNameChar
	case 255:
		return "GEOMETRY"
	}
	return ""
}

// DecodeTextValue decodes one buffered text row field without retaining packet bytes.
func DecodeTextValue(c wire.Column, mode config.Mode, data []byte) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	if TextTypeName(c, mode) == "" {
		return nil, &dberrors.UnsupportedTypeError{Type: c.Type, Mode: mode}
	}
	switch c.Type {
	case 0, 246:
		return string(data), nil
	case 206:
		if _, _, err := valueformat.DecimalParts(string(data)); err != nil {
			return nil, err
		}
		return string(data), nil
	case 1, 2, 3, 8, 9, 13:
		if mode == config.ModeOracle && c.Type == 3 {
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
		if err != nil || (mode != config.ModeOracle && (math.IsNaN(n) || math.IsInf(n, 0))) {
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
	name, known := charset.Collation(c.Charset)
	if !known {
		return nil, fmt.Errorf("%w: result character set", dberrors.ErrUnsupported)
	}
	return charset.Decode(name, data)
}
