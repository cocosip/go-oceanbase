package valuecodec

import (
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// EncodeParameter encodes a scalar parameter within its payload budget.
func EncodeParameter(v any, mode config.Mode, options config.SessionOptions, limit int64) (wire.Parameter, error) {
	switch v.(type) {
	case types.Number, types.NString, types.Raw, types.RowID, types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
		return encodeScalarParameter(v, mode, options, limit)
	}
	p := wire.Parameter{}
	switch v := v.(type) {
	case nil:
		p.Type = 6
	case int64:
		p.Type = 8
		p.Data = binary.LittleEndian.AppendUint64(nil, uint64(v))
	case uint64:
		p.Type = 8 | 128<<8
		p.Data = binary.LittleEndian.AppendUint64(nil, v)
	case float64:
		if mode != config.ModeOracle && (math.IsNaN(v) || math.IsInf(v, 0)) {
			return p, fmt.Errorf("%w: non-finite parameter", dberrors.ErrBind)
		}
		p.Type = 5
		p.Data = binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))
	case bool:
		p.Type = 1
		p.Data = []byte{0}
		if v {
			p.Data[0] = 1
		}
	case []byte:
		if v == nil {
			p.Type = 6
			break
		}
		if int64(len(v)) > limit {
			return p, wire.ErrMessageTooLarge
		}
		p.Type = 252
		p.Data = v
		if mode == config.ModeOracle {
			if int64(len(v)) > limit/2 || len(v) > math.MaxInt/2 {
				return wire.Parameter{}, wire.ErrMessageTooLarge
			}
			// Connector/J binds Oracle byte arrays as hex text, including BLOB inputs.
			p.Type, p.Data = 253, make([]byte, hex.EncodedLen(len(v)))
			hex.Encode(p.Data, v)
		}
		p.LengthEncoded = true
	case string:
		if int64(utf8.RuneCountInString(v)) > limit {
			return p, wire.ErrMessageTooLarge
		}
		if !utf8.ValidString(v) {
			return p, fmt.Errorf("%w: invalid UTF-8 parameter", dberrors.ErrBind)
		}
		name := options.Charset
		if name == "" {
			name = charsetUTF8MB4
		}
		data, err := charset.Encode(name, v)
		if err != nil {
			return p, err
		}
		if int64(len(data)) > limit {
			return p, wire.ErrMessageTooLarge
		}
		p.Type = 253
		p.Data = data
		p.LengthEncoded = true
	case time.Time:
		if mode == config.ModeOracle {
			return encodeTemporal(v, options)
		}
		if options.Location == nil {
			options.Location = time.UTC
		}
		v = v.In(options.Location)
		if v.Year() < 1 || v.Year() > 9999 {
			return p, fmt.Errorf("%w: datetime range", dberrors.ErrBind)
		}
		p.Type = 12
		p.Data = []byte{11, byte(v.Year()), byte(v.Year() >> 8), byte(v.Month()), byte(v.Day()), byte(v.Hour()), byte(v.Minute()), byte(v.Second())}
		p.Data = binary.LittleEndian.AppendUint32(p.Data, uint32(v.Nanosecond()/1000))
	default:
		return p, fmt.Errorf("%w: binary parameter type", dberrors.ErrBind)
	}
	return p, nil
}

func encodeScalarParameter(v any, mode config.Mode, options config.SessionOptions, limit int64) (wire.Parameter, error) {
	p := wire.Parameter{}
	switch v := v.(type) {
	case types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
		if mode != config.ModeOracle {
			return p, dberrors.ErrUnsupported
		}
		var err error
		p, err = encodeTemporal(v, options)
		if err != nil {
			return p, err
		}
		if int64(len(p.Data)) > limit {
			return wire.Parameter{}, wire.ErrMessageTooLarge
		}
	case types.Number:
		text, err := valueformat.PlainNumber(string(v), limit)
		if err != nil {
			return p, err
		}
		p.Type, p.LengthEncoded, p.Data = 246, true, []byte(text)
	case types.NString, types.Raw, types.RowID:
		if mode != config.ModeOracle {
			return p, dberrors.ErrUnsupported
		}
		p.LengthEncoded = true
		switch v := v.(type) {
		case types.NString:
			if int64(utf8.RuneCountInString(string(v))) > limit {
				return p, wire.ErrMessageTooLarge
			}
			name := options.NCharset
			if name == "" {
				name = options.Charset
			}
			if name == "" {
				name = charsetUTF8MB4
			}
			data, err := charset.Encode(name, string(v))
			if err != nil {
				return p, err
			}
			p.Type, p.Data = 208, data
		case types.Raw:
			p.Type, p.Data = 203, []byte(v)
			if v == nil {
				p.Type = 6
			}
		case types.RowID:
			if _, err := v.Value(); err != nil {
				return p, err
			}
			p.Type, p.Data = 253, []byte(v)
		}
		if int64(len(p.Data)) > limit {
			return wire.Parameter{}, wire.ErrMessageTooLarge
		}
	default:
		return p, dberrors.ErrType
	}
	return p, nil
}

// DecodeBinaryValue decodes one buffered binary row field.
func DecodeBinaryValue(c wire.Column, mode config.Mode, data []byte, session ...config.SessionOptions) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	switch c.Type {
	case 1, 2, 3, 8, 9, 13:
		var n uint64
		bits := 0
		switch len(data) {
		case 1:
			n = uint64(data[0])
			bits = 8
		case 2:
			n = uint64(binary.LittleEndian.Uint16(data))
			bits = 16
		case 4:
			n = uint64(binary.LittleEndian.Uint32(data))
			bits = 32
		case 8:
			n = binary.LittleEndian.Uint64(data)
			bits = 64
		default:
			return nil, wire.ErrMalformed
		}
		if c.Flags&32 != 0 {
			if n > math.MaxInt64 {
				return strconv.FormatUint(n, 10), nil
			}
			return int64(n), nil
		}
		signed := int64(n<<uint(64-bits)) >> uint(64-bits)
		if c.Type == 3 && mode == config.ModeOracle {
			return strconv.FormatInt(signed, 10), nil
		}
		return signed, nil
	case 4:
		if len(data) != 4 {
			return nil, wire.ErrMalformed
		}
		return binaryFloatValue(float64(math.Float32frombits(binary.LittleEndian.Uint32(data))), mode)
	case 5:
		if len(data) != 8 {
			return nil, wire.ErrMalformed
		}
		return binaryFloatValue(math.Float64frombits(binary.LittleEndian.Uint64(data)), mode)
	case 7, 10, 11, 12, 14:
		text, err := binaryTimeText(c.Type, data)
		if err != nil {
			return nil, err
		}
		return mysqlBinaryTemporalText(c, mode, text, session...), nil
	default:
		return DecodeTextValue(c, mode, data, session...)
	}
}

func mysqlBinaryTemporalText(c wire.Column, mode config.Mode, text string, session ...config.SessionOptions) string {
	text = mysqlTemporalText(mode, c.Type, text, session...)
	if mode != config.ModeMySQL || len(session) == 0 || session[0].CompatibleMysqlVersion != 8 {
		return text
	}
	dot := strings.LastIndexByte(text, '.')
	if dot < 0 || dot+1 == len(text) || c.Decimals == 0 {
		return text
	}
	fraction := text[dot+1:]
	precision := int(c.Decimals)
	if len(fraction) <= precision {
		return text
	}
	trimmed := strings.TrimRight(fraction, "0")
	if len(trimmed) > precision {
		return text
	}
	return text[:dot+1] + trimmed + strings.Repeat("0", precision-len(trimmed))
}

func binaryTimeText(typ byte, data []byte) (string, error) {
	if typ == 11 {
		return binaryDurationText(data)
	}
	if len(data) == 0 {
		if typ == 10 || typ == 14 {
			return "0000-00-00", nil
		}
		return "0000-00-00 00:00:00", nil
	}
	if len(data) != 4 && len(data) != 7 && len(data) != 11 {
		return "", wire.ErrMalformed
	}
	if (typ == 10 || typ == 14) && len(data) != 4 {
		return "", wire.ErrMalformed
	}
	year := binary.LittleEndian.Uint16(data)
	month, day := data[2], data[3]
	if year > 9999 || month > 12 || day > 31 {
		return "", wire.ErrMalformed
	}
	text := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	if typ == 10 || typ == 14 {
		return text, nil
	}
	hour, minute, second := byte(0), byte(0), byte(0)
	if len(data) >= 7 {
		hour, minute, second = data[4], data[5], data[6]
	}
	if hour > 23 || minute > 59 || second > 59 {
		return "", wire.ErrMalformed
	}
	text += fmt.Sprintf(" %02d:%02d:%02d", hour, minute, second)
	if len(data) == 11 {
		fraction, err := binaryFraction(data[7:])
		if err != nil {
			return "", err
		}
		text += fraction
	}
	return text, nil
}
func binaryFraction(data []byte) (string, error) {
	n := binary.LittleEndian.Uint32(data)
	if n > 999999 {
		return "", wire.ErrMalformed
	}
	return fmt.Sprintf(".%06d", n), nil
}
func binaryDurationText(data []byte) (string, error) {
	if len(data) == 0 {
		return "00:00:00", nil
	}
	if len(data) != 8 && len(data) != 12 {
		return "", wire.ErrMalformed
	}
	if data[0] > 1 || data[5] > 23 || data[6] > 59 || data[7] > 59 {
		return "", wire.ErrMalformed
	}
	hours := uint64(binary.LittleEndian.Uint32(data[1:]))*24 + uint64(data[5])
	sign := ""
	if data[0] == 1 {
		sign = "-"
	}
	text := fmt.Sprintf("%s%02d:%02d:%02d", sign, hours, data[6], data[7])
	if len(data) == 12 {
		fraction, err := binaryFraction(data[8:])
		if err != nil {
			return "", err
		}
		text += fraction
	}
	return text, nil
}

func binaryFloatValue(n float64, mode config.Mode) (driver.Value, error) {
	if mode != config.ModeOracle && (math.IsNaN(n) || math.IsInf(n, 0)) {
		return nil, fmt.Errorf("oceanbase: invalid floating point value")
	}
	return n, nil
}
