package oceanbase

import (
	"database/sql/driver"
	"strconv"
	"strings"
	"time"
)

// TimestampTZ preserves a timestamp's fraction, offset, region and abbreviation.
type TimestampTZ string

// TimestampLTZ retains a timestamp normalized to the explicitly configured session zone.
type TimestampLTZ string

// IntervalYM preserves a signed year and month interval without approximating a duration.
type IntervalYM string

// IntervalDS preserves a signed day and second interval with up to nine fractional digits.
type IntervalDS string

// Scan reads a canonical timestamp.
func (t *TimestampTZ) Scan(src any) error { return scanTyped(t, src, TimestampTZ.Value) }

// Value implements driver.Valuer.
func (t TimestampTZ) Value() (driver.Value, error) {
	_, err := parseTimestamp(string(t))
	if err != nil {
		return nil, err
	}
	return string(t), nil
}

// Time resolves the explicit offset and retains a known region's time zone.
func (t TimestampTZ) Time() (time.Time, error) {
	p, err := parseTimestamp(string(t))
	return p.instant, err
}

// Scan reads a canonical session timestamp.
func (t *TimestampLTZ) Scan(src any) error { return scanTyped(t, src, TimestampLTZ.Value) }

// Value implements driver.Valuer.
func (t TimestampLTZ) Value() (driver.Value, error) {
	_, err := parseLTZ(string(t))
	if err != nil {
		return nil, err
	}
	return string(t), nil
}

// Time resolves the explicit session offset.
func (t TimestampLTZ) Time() (time.Time, error) {
	p, err := parseLTZ(string(t))
	if err != nil {
		return time.Time{}, err
	}
	if !p.local {
		return p.instant, nil
	}
	loc, err := time.LoadLocation(p.region)
	if err != nil {
		return time.Time{}, ErrType
	}
	return resolveLocal(p.instant, loc)
}

// Scan reads a year and month interval.
func (i *IntervalYM) Scan(src any) error { return scanTyped(i, src, IntervalYM.Value) }

// Value implements driver.Valuer.
func (i IntervalYM) Value() (driver.Value, error) {
	_, err := parseInterval(string(i), false)
	if err != nil {
		return nil, err
	}
	return string(i), nil
}

// Scan reads a day and second interval.
func (i *IntervalDS) Scan(src any) error { return scanTyped(i, src, IntervalDS.Value) }

// Value implements driver.Valuer.
func (i IntervalDS) Value() (driver.Value, error) {
	_, err := parseInterval(string(i), true)
	if err != nil {
		return nil, err
	}
	return string(i), nil
}

const timestampLayout = "2006-01-02 15:04:05"

type timestampParts struct {
	instant      time.Time
	scale        int
	region, abbr string
	offset       int
	local        bool
}

func parseLTZ(text string) (timestampParts, error) {
	if len(text) > 240 {
		return timestampParts{}, ErrType
	}
	parts := strings.Split(text, " ")
	if len(parts) != 3 || strings.HasPrefix(parts[2], "+") || strings.HasPrefix(parts[2], "-") {
		return parseTimestamp(text)
	}
	wall, scale, err := parseWall(parts[0] + " " + parts[1])
	if err != nil || !zoneToken(parts[2], 128) {
		return timestampParts{}, ErrType
	}
	return timestampParts{instant: wall, scale: scale, region: parts[2], local: true}, nil
}

func ltzSessionWall(p timestampParts, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	instant := p.instant
	if p.local {
		zone, err := sessionTimeZone(location)
		if err != nil {
			return time.Time{}, err
		}
		if p.region == zone {
			return instant, nil
		}
		source, err := time.LoadLocation(p.region)
		if err != nil {
			return time.Time{}, ErrType
		}
		instant, err = resolveLocal(instant, source)
		if err != nil {
			return time.Time{}, err
		}
	}
	wall := instant.In(location)
	if wall.Year() < 1 || wall.Year() > 9999 {
		return time.Time{}, ErrType
	}
	if _, err := resolveLocal(wall, location); err != nil {
		return time.Time{}, err
	}
	return wall, nil
}

func resolveLocal(wall time.Time, loc *time.Location) (time.Time, error) {
	local := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	start, end := local.ZoneBounds()
	candidates := []time.Time{local}
	if !start.IsZero() {
		candidates = append(candidates, start.Add(-time.Nanosecond))
	}
	if !end.IsZero() {
		candidates = append(candidates, end)
	}
	var found time.Time
	hasCandidate := false
	for _, candidate := range candidates {
		_, offset := candidate.Zone()
		utc := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.UTC)
		instant := utc.Add(-time.Duration(offset) * time.Second).In(loc)
		if instant.Format(timestampLayout+".000000000") != wall.Format(timestampLayout+".000000000") {
			continue
		}
		if hasCandidate && !found.Equal(instant) {
			return time.Time{}, ErrType
		}
		found = instant
		hasCandidate = true
	}
	if !hasCandidate {
		return time.Time{}, ErrType
	}
	return found, nil
}

func parseTimestamp(text string) (timestampParts, error) {
	var p timestampParts
	if len(text) > 240 {
		return p, ErrType
	}
	parts := strings.Split(text, " ")
	if len(parts) < 3 || len(parts) > 5 {
		return p, ErrType
	}
	wall, scale, err := parseWall(parts[0] + " " + parts[1])
	if err != nil {
		return p, err
	}
	offset := parts[2]
	if len(offset) != 6 || offset[3] != ':' || (offset[0] != '+' && offset[0] != '-') {
		return p, ErrType
	}
	hour, err := decimalField(offset[1:3], 14)
	if err != nil {
		return p, err
	}
	minute, err := decimalField(offset[4:], 59)
	if err != nil {
		return p, err
	}
	p.offset = hour*3600 + minute*60
	if offset[0] == '-' {
		p.offset = -p.offset
	}
	if p.offset < -12*3600 || p.offset > 14*3600 {
		return p, ErrType
	}
	p.scale = scale
	p.instant = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.FixedZone("", p.offset))
	if len(parts) > 3 {
		p.region = parts[3]
		if !zoneToken(p.region, 128) {
			return p, ErrType
		}
		if loc, loadErr := time.LoadLocation(p.region); loadErr == nil {
			local := p.instant.In(loc)
			_, actual := local.Zone()
			if actual != p.offset {
				return p, ErrType
			}
			p.instant = local
		}
	}
	if len(parts) > 4 {
		p.abbr = parts[4]
		if !zoneToken(p.abbr, 64) {
			return p, ErrType
		}
	}
	return p, nil
}

func zoneToken(text string, limit int) bool {
	if len(text) == 0 || len(text) > limit {
		return false
	}
	for _, c := range text {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '/' || c == '_' || c == '-' || c == '+' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func parseWall(text string) (time.Time, int, error) {
	if len(text) < 19 || len(text) > 29 {
		return time.Time{}, 0, ErrType
	}
	scale, err := fractionScale(text[19:])
	if err != nil {
		return time.Time{}, 0, err
	}
	t, err := time.Parse(timestampLayout, text)
	if err != nil || t.Year() < 1 {
		return time.Time{}, 0, ErrType
	}
	return t, scale, nil
}

func fractionScale(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	if len(text) < 2 || len(text) > 10 || text[0] != '.' {
		return 0, ErrType
	}
	if _, err := decimalField(text[1:], 999999999); err != nil {
		return 0, err
	}
	return len(text) - 1, nil
}

func decimalField(text string, maxValue int) (int, error) {
	if len(text) == 0 || len(text) > 9 {
		return 0, ErrType
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, ErrType
		}
	}
	// Atoi returns int directly, so no narrowing conversion can overflow; on 32-bit
	// targets it reports ErrRange instead of wrapping.
	n, err := strconv.Atoi(text)
	if err != nil || n > maxValue {
		return 0, ErrType
	}
	return n, nil
}

// intervalParts holds fields already narrowed to their wire widths. parseInterval
// performs the range checks, so encodeInterval never converts a wider integer.
type intervalParts struct {
	negative             bool
	major, nanos         uint32
	minor, precision     byte
	hour, minute, second byte
	scale                byte
}

func parseInterval(text string, days bool) (intervalParts, error) {
	var p intervalParts
	if len(text) > 30 {
		return p, ErrType
	}
	if strings.HasPrefix(text, "-") {
		p.negative = true
		text = text[1:]
	} else {
		text = strings.TrimPrefix(text, "+")
	}
	delim := "-"
	if days {
		delim = " "
	}
	parts := strings.Split(text, delim)
	if len(parts) != 2 {
		return p, ErrType
	}
	if len(parts[0]) > 0xff {
		return p, ErrType
	}
	p.precision = byte(len(parts[0]))
	major, err := decimalField(parts[0], 999999999)
	if err != nil || major < 0 || major > 999999999 {
		return p, ErrType
	}
	p.major = uint32(major)
	if !days {
		minor, err := decimalField(parts[1], 11)
		if err != nil || minor < 0 || minor > 11 {
			return p, ErrType
		}
		p.minor = byte(minor)
		return p, nil
	}
	clock := parts[1]
	if len(clock) < 8 {
		return p, ErrType
	}
	scale, err := fractionScale(clock[8:])
	if err != nil {
		return p, err
	}
	if scale > 0xff {
		return p, ErrType
	}
	p.scale = byte(scale)
	t, err := time.Parse("15:04:05", clock)
	if err != nil {
		return p, ErrType
	}
	// time.Parse bounds these to 0-23, 0-59 and 0-59, so each fits a byte.
	p.hour, p.minute, p.second = byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	p.nanos = uint32(t.Nanosecond())
	return p, nil
}
