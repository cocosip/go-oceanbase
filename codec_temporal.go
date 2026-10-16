package oceanbase

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) decodeRowValue(column wire.Column, binaryRows bool, data []byte) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	if c.session.mode == ModeOracle && oracleTemporal(column.Type) {
		value, err := decodeOracleTemporal(column.Type, data, c.cfg.Session.Location)
		if err == nil && column.Type == 202 && c.cfg.Session.ParseTime {
			return c.parseRowTime(value)
		}
		return value, err
	}
	if c.session.mode == ModeOracle && (column.Type == 207 || column.Type == 208) {
		name := c.cfg.Session.NCharset
		if name == "" {
			name = c.cfg.Session.Charset
		}
		if name == "" {
			name = charsetUTF8MB4
		}
		return charset.Decode(name, data)
	}
	if column.Type == 209 && c.session.mode == ModeOracle {
		if _, err := RowID(string(data)).Value(); err != nil {
			return nil, err
		}
		return string(data), nil
	}
	var value driver.Value
	var err error
	if binaryRows {
		value, err = decodeBinaryValue(column, c.session.mode, data)
	} else {
		value, err = decodeTextValue(column, c.session.mode, data)
	}
	if err != nil || !c.cfg.Session.ParseTime {
		return value, err
	}
	if column.Type == 7 || column.Type == 10 || column.Type == 12 || column.Type == 14 {
		text, ok := value.(string)
		if !ok {
			return nil, ErrType
		}
		return c.parseRowTime(text)
	}
	return value, nil
}

func (c *conn) parseRowTime(text string) (time.Time, error) {
	if len(text) == 10 {
		text += " 00:00:00"
	}
	wall, _, err := parseWall(text)
	if err != nil {
		return time.Time{}, err
	}
	loc := c.cfg.Session.Location
	if loc == nil {
		loc = time.UTC
	}
	return resolveLocal(wall, loc)
}

func oracleTemporal(typ byte) bool {
	return typ == 200 || typ == 201 || typ == 202 || typ == 204 || typ == 205
}

func decodeOracleTemporal(typ byte, data []byte, location *time.Location) (string, error) {
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
	t, _, err := parseWall(wall)
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
	if _, err := parseTimestamp(text); err != nil {
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
	if _, err := parseInterval(text, typ == 205); err != nil {
		return "", err
	}
	return text, nil
}

func encodeOracleWall(t time.Time, scale int) ([]byte, error) {
	if t.Year() < 1 || t.Year() > 9999 {
		return nil, ErrType
	}
	if _, err := scaledFraction(uint32(t.Nanosecond()), scale); err != nil {
		return nil, err
	}
	data := []byte{byte(t.Year() / 100), byte(t.Year() % 100), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())}
	data = binary.LittleEndian.AppendUint32(data, uint32(t.Nanosecond()))
	return append(data, byte(scale)), nil
}

func encodeTemporal(v any, options SessionOptions) (wire.Parameter, error) {
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
	case TimestampLTZ:
		p.Type = 201
		timestamp, err := parseLTZ(string(value))
		if err != nil {
			return p, err
		}
		wall := timestamp.instant
		if !timestamp.local || timestamp.region != options.Location.String() {
			instant, timeErr := value.Time()
			if timeErr != nil {
				return p, timeErr
			}
			wall = instant.In(options.Location)
			if _, timeErr = resolveLocal(wall, options.Location); timeErr != nil {
				return p, timeErr
			}
		}
		p.Data, err = encodeOracleWall(wall, timestamp.scale)
		return p, err
	case TimestampTZ:
		text := fmt.Sprint(value)
		timestamp, err := parseTimestamp(text)
		if err != nil {
			return p, err
		}
		p.Type = 200
		p.Data, err = encodeOracleWall(timestamp.instant, timestamp.scale)
		if err != nil {
			return p, err
		}
		hour := timestamp.offset / 3600
		minute := timestamp.offset % 3600 / 60
		if hour < 0 {
			minute = -minute
		}
		p.Data = append(p.Data, byte(hour), byte(minute), byte(len(timestamp.region)))
		p.Data = append(p.Data, timestamp.region...)
		p.Data = append(p.Data, byte(len(timestamp.abbr)))
		p.Data = append(p.Data, timestamp.abbr...)
		return p, nil
	case IntervalYM:
		p.Type = 204
		parts, err := parseInterval(string(value), false)
		if err != nil {
			return p, err
		}
		p.Data = encodeInterval(parts, false)
		return p, nil
	case IntervalDS:
		p.Type = 205
		parts, err := parseInterval(string(value), true)
		if err != nil {
			return p, err
		}
		p.Data = encodeInterval(parts, true)
		return p, nil
	}
	return p, ErrType
}

func encodeInterval(p intervalParts, days bool) []byte {
	data := []byte{0}
	if p.negative {
		data[0] = 1
	}
	data = binary.LittleEndian.AppendUint32(data, uint32(p.major))
	if !days {
		return append(data, byte(p.minor), byte(p.precision))
	}
	data = append(data, byte(p.hour), byte(p.minute), byte(p.second))
	data = binary.LittleEndian.AppendUint32(data, uint32(p.nanos))
	return append(data, byte(p.precision), byte(p.scale))
}
