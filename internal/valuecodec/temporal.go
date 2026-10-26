package valuecodec

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// DecodeRowValue decodes one row field using the explicit mode and session options.
func DecodeRowValue(column wire.Column, binaryRows bool, data []byte, mode config.Mode, options config.SessionOptions) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	if mode == config.ModeOracle && oracleTemporal(column.Type) {
		value, err := DecodeOracleTemporal(column.Type, data, options.Location)
		if err == nil && options.ParseTime {
			switch column.Type {
			case 200:
				return types.TimestampTZ(value).Time()
			case 201:
				parts, err := valueformat.ParseLTZ(value)
				if err != nil {
					return nil, err
				}
				location := options.Location
				if location == nil {
					location = time.UTC
				}
				if parts.Local {
					return valueformat.ResolveLocal(parts.Instant, location)
				}
				return parts.Instant.In(location), nil
			case 202:
				return ParseRowTime(value, options.Location)
			}
		}
		return value, err
	}
	if mode == config.ModeOracle && (column.Type == 207 || column.Type == 208) {
		name := options.NCharset
		if name == "" {
			name = options.Charset
		}
		if name == "" {
			name = charsetUTF8MB4
		}
		return charset.Decode(name, data)
	}
	if column.Type == 209 && mode == config.ModeOracle {
		if _, err := types.RowID(string(data)).Value(); err != nil {
			return nil, err
		}
		return string(data), nil
	}
	var value driver.Value
	var err error
	if binaryRows {
		value, err = DecodeBinaryValue(column, mode, data, options)
	} else {
		value, err = DecodeTextValue(column, mode, data, options)
	}
	if err != nil || !options.ParseTime {
		return value, err
	}
	if column.Type == 7 || column.Type == 10 || column.Type == 12 || column.Type == 14 {
		text, ok := value.(string)
		if !ok {
			return nil, dberrors.ErrType
		}
		return ParseRowTime(text, options.Location)
	}
	return value, nil
}

// ParseRowTime resolves a SQL wall time in the session location.
func ParseRowTime(text string, location *time.Location) (time.Time, error) {
	if len(text) == 10 {
		text += " 00:00:00"
	}
	wall, _, err := valueformat.ParseWall(text)
	if err != nil {
		return time.Time{}, err
	}
	loc := location
	if loc == nil {
		loc = time.UTC
	}
	return valueformat.ResolveLocal(wall, loc)
}

func oracleTemporal(typ byte) bool {
	return typ == 200 || typ == 201 || typ == 202 || typ == 204 || typ == 205
}

// DecodeOracleTemporal decodes an Oracle timestamp or interval while retaining precision.
func DecodeOracleTemporal(typ byte, data []byte, location *time.Location) (string, error) {
	if typ == 204 || typ == 205 {
		return decodeInterval(typ, data)
	}
	if len(data) < 12 {
		return "", wire.ErrMalformed
	}
	if typ != 200 && len(data) != 12 {
		return "", wire.ErrMalformed
	}
	year := int(data[0])*100 + int(data[1])
	nanos := binary.LittleEndian.Uint32(data[7:])
	scale := int(data[11])
	if data[1] > 99 || nanos > 999999999 || scale > 9 {
		return "", wire.ErrMalformed
	}
	fraction, err := scaledFraction(nanos, scale)
	if err != nil {
		return "", err
	}
	wall := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d%s", year, data[2], data[3], data[4], data[5], data[6], fraction)
	t, _, err := valueformat.ParseWall(wall)
	if err != nil {
		return "", err
	}
	switch typ {
	case 200:
		return decodeTimestampZone(wall, data)
	case 201:
		if location == nil {
			location = time.UTC
		}
		local := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), location)
		start, end := local.ZoneBounds()
		if !start.IsZero() || !end.IsZero() {
			return wall + " " + location.String(), nil
		}
		return wall + " " + local.Format("-07:00"), nil
	default:
		return wall, nil
	}
}

func decodeTimestampZone(wall string, data []byte) (string, error) {
	if len(data) < 14 {
		return "", wire.ErrMalformed
	}
	hour, minute := int(int8(data[12])), int(int8(data[13]))
	if hour < -12 || hour > 14 || minute < -59 || minute > 59 || hour > 0 && minute < 0 {
		return "", wire.ErrMalformed
	}
	sign := "+"
	if hour < 0 || minute < 0 {
		sign = "-"
	}
	if hour < 0 {
		hour = -hour
	}
	if minute < 0 {
		minute = -minute
	}
	text := fmt.Sprintf("%s %s%02d:%02d", wall, sign, hour, minute)
	if len(data) > 14 {
		n := int(data[14])
		end := 15 + n
		if end >= len(data) || int(data[end])+end+1 != len(data) {
			return "", wire.ErrMalformed
		}
		if n > 0 {
			text += " " + string(data[15:end])
		}
		if data[end] > 0 {
			if n == 0 {
				return "", wire.ErrMalformed
			}
			text += " " + string(data[end+1:])
		}
	}
	if _, err := valueformat.ParseTimestamp(text); err != nil {
		return "", err
	}
	return text, nil
}

func scaledFraction(nanos uint32, scale int) (string, error) {
	if nanos > 999999999 || scale < 0 || scale > 9 {
		return "", wire.ErrMalformed
	}
	text := fmt.Sprintf("%09d", nanos)
	if strings.Trim(text[scale:], "0") != "" {
		return "", wire.ErrMalformed
	}
	if scale == 0 {
		return "", nil
	}
	return "." + text[:scale], nil
}

func decodeInterval(typ byte, data []byte) (string, error) {
	length := 7
	if typ == 205 {
		length = 14
	}
	if len(data) != length || data[0] > 1 {
		return "", wire.ErrMalformed
	}
	major := binary.LittleEndian.Uint32(data[1:])
	precision := int(data[6])
	if typ == 205 {
		precision = int(data[12])
	}
	if major > 999999999 || precision > 9 || precision == 0 && major != 0 {
		return "", wire.ErrMalformed
	}
	if precision == 0 {
		precision = 1
	}
	text := fmt.Sprintf("%0*d", precision, major)
	if len(text) > precision {
		return "", wire.ErrMalformed
	}
	if typ == 204 {
		text += fmt.Sprintf("-%02d", data[5])
	} else {
		fraction, err := scaledFraction(binary.LittleEndian.Uint32(data[8:]), int(data[13]))
		if err != nil {
			return "", err
		}
		text += fmt.Sprintf(" %02d:%02d:%02d%s", data[5], data[6], data[7], fraction)
	}
	if data[0] == 1 {
		text = "-" + text
	}
	if _, err := valueformat.ParseInterval(text, typ == 205); err != nil {
		return "", err
	}
	return text, nil
}

func encodeOracleWall(t time.Time, scale int) ([]byte, error) {
	if t.Year() < 1 || t.Year() > 9999 {
		return nil, dberrors.ErrType
	}
	if _, err := scaledFraction(uint32(t.Nanosecond()), scale); err != nil {
		return nil, err
	}
	data := []byte{byte(t.Year() / 100), byte(t.Year() % 100), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())}
	data = binary.LittleEndian.AppendUint32(data, uint32(t.Nanosecond()))
	return append(data, byte(scale)), nil
}

func encodeTemporal(v any, options config.SessionOptions) (wire.Parameter, error) {
	p := wire.Parameter{LengthEncoded: true}
	if options.Location == nil {
		options.Location = time.UTC
	}
	switch value := v.(type) {
	case time.Time:
		p.Type = 202
		data, err := encodeOracleWall(value.In(options.Location), 9)
		p.Data = data
		return p, err
	case types.TimestampLTZ:
		p.Type = 201
		timestamp, err := valueformat.ParseLTZ(string(value))
		if err != nil {
			return p, err
		}
		wall, err := ltzSessionWall(timestamp, options.Location)
		if err != nil {
			return p, err
		}
		p.Data, err = encodeOracleWall(wall, timestamp.Scale)
		return p, err
	case types.TimestampTZ:
		text := fmt.Sprint(value)
		timestamp, err := valueformat.ParseTimestamp(text)
		if err != nil {
			return p, err
		}
		p.Type = 200
		p.Data, err = encodeOracleWall(timestamp.Instant, timestamp.Scale)
		if err != nil {
			return p, err
		}
		hour := timestamp.Offset / 3600
		minute := timestamp.Offset % 3600 / 60
		if hour < 0 {
			minute = -minute
		}
		p.Data = append(p.Data, byte(hour), byte(minute), byte(len(timestamp.Region)))
		p.Data = append(p.Data, timestamp.Region...)
		p.Data = append(p.Data, byte(len(timestamp.Abbr)))
		p.Data = append(p.Data, timestamp.Abbr...)
		return p, nil
	case types.IntervalYM:
		p.Type = 204
		parts, err := valueformat.ParseInterval(string(value), false)
		if err != nil {
			return p, err
		}
		p.Data = encodeInterval(parts, false)
		return p, nil
	case types.IntervalDS:
		p.Type = 205
		parts, err := valueformat.ParseInterval(string(value), true)
		if err != nil {
			return p, err
		}
		p.Data = encodeInterval(parts, true)
		return p, nil
	}
	return p, dberrors.ErrType
}

// encodeInterval packs an interval into the Oracle binary interval layout.
// valueformat.IntervalParts already stores each field at its wire width, so no value is
// narrowed here.
func encodeInterval(p valueformat.IntervalParts, days bool) []byte {
	data := []byte{0}
	if p.Negative {
		data[0] = 1
	}
	data = binary.LittleEndian.AppendUint32(data, p.Major)
	if !days {
		return append(data, p.Minor, p.Precision)
	}
	data = append(data, p.Hour, p.Minute, p.Second)
	data = binary.LittleEndian.AppendUint32(data, p.Nanos)
	return append(data, p.Precision, p.Scale)
}

func ltzSessionWall(p valueformat.TimestampParts, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	instant := p.Instant
	if p.Local {
		zone, ok := configspec.SessionTimeZone(location)
		if !ok {
			return time.Time{}, &config.ConfigError{Field: "Session.Location"}
		}
		if p.Region == zone {
			return instant, nil
		}
		source, err := time.LoadLocation(p.Region)
		if err != nil {
			return time.Time{}, dberrors.ErrType
		}
		instant, err = valueformat.ResolveLocal(instant, source)
		if err != nil {
			return time.Time{}, err
		}
	}
	wall := instant.In(location)
	if wall.Year() < 1 || wall.Year() > 9999 {
		return time.Time{}, dberrors.ErrType
	}
	if _, err := valueformat.ResolveLocal(wall, location); err != nil {
		return time.Time{}, err
	}
	return wall, nil
}
