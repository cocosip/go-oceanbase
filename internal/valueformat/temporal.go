package valueformat

import (
	"strconv"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/configspec"
)

// TimestampLayout is the whole-second form shared by timestamp values.
const TimestampLayout = "2006-01-02 15:04:05"

// TimestampParts retains the exact fraction scale and explicit zone metadata.
type TimestampParts struct {
	Instant      time.Time
	Scale        int
	Region, Abbr string
	Offset       int
	Local        bool
}

// IntervalParts stores validated fields at their wire widths.
type IntervalParts struct {
	Negative             bool
	Major, Nanos         uint32
	Minor, Precision     byte
	Hour, Minute, Second byte
	Scale                byte
}

// ParseLTZ validates an LTZ timestamp while retaining an unresolved session region.
func ParseLTZ(text string) (TimestampParts, error) {
	if len(text) > 240 {
		return TimestampParts{}, dberrors.ErrType
	}
	parts := strings.Split(text, " ")
	if len(parts) != 3 || strings.HasPrefix(parts[2], "+") || strings.HasPrefix(parts[2], "-") {
		return ParseTimestamp(text)
	}
	wall, scale, err := ParseWall(parts[0] + " " + parts[1])
	if err != nil || !configspec.ZoneToken(parts[2], 128) {
		return TimestampParts{}, dberrors.ErrType
	}
	return TimestampParts{Instant: wall, Scale: scale, Region: parts[2], Local: true}, nil
}

// ResolveLocal rejects missing or ambiguous wall times in a location.
func ResolveLocal(wall time.Time, loc *time.Location) (time.Time, error) {
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
		if instant.Format(TimestampLayout+".000000000") != wall.Format(TimestampLayout+".000000000") {
			continue
		}
		if hasCandidate && !found.Equal(instant) {
			return time.Time{}, dberrors.ErrType
		}
		found = instant
		hasCandidate = true
	}
	if !hasCandidate {
		return time.Time{}, dberrors.ErrType
	}
	return found, nil
}

// ParseTimestamp validates a timestamp with an explicit offset and optional zone metadata.
func ParseTimestamp(text string) (TimestampParts, error) {
	var p TimestampParts
	if len(text) > 240 {
		return p, dberrors.ErrType
	}
	parts := strings.Split(text, " ")
	if len(parts) < 3 || len(parts) > 5 {
		return p, dberrors.ErrType
	}
	wall, scale, err := ParseWall(parts[0] + " " + parts[1])
	if err != nil {
		return p, err
	}
	offset := parts[2]
	if len(offset) != 6 || offset[3] != ':' || (offset[0] != '+' && offset[0] != '-') {
		return p, dberrors.ErrType
	}
	hour, err := DecimalField(offset[1:3], 14)
	if err != nil {
		return p, err
	}
	minute, err := DecimalField(offset[4:], 59)
	if err != nil {
		return p, err
	}
	p.Offset = hour*3600 + minute*60
	if offset[0] == '-' {
		p.Offset = -p.Offset
	}
	if p.Offset < -12*3600 || p.Offset > 14*3600 {
		return p, dberrors.ErrType
	}
	p.Scale = scale
	p.Instant = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.FixedZone("", p.Offset))
	if len(parts) > 3 {
		p.Region = parts[3]
		if !configspec.ZoneToken(p.Region, 128) {
			return p, dberrors.ErrType
		}
		if loc, loadErr := time.LoadLocation(p.Region); loadErr == nil {
			local := p.Instant.In(loc)
			_, actual := local.Zone()
			if actual != p.Offset {
				return p, dberrors.ErrType
			}
			p.Instant = local
		}
	}
	if len(parts) > 4 {
		p.Abbr = parts[4]
		if !configspec.ZoneToken(p.Abbr, 64) {
			return p, dberrors.ErrType
		}
	}
	return p, nil
}

// ParseWall validates a wall-clock timestamp and its fractional precision.
func ParseWall(text string) (time.Time, int, error) {
	if len(text) < 19 || len(text) > 29 {
		return time.Time{}, 0, dberrors.ErrType
	}
	scale, err := FractionScale(text[19:])
	if err != nil {
		return time.Time{}, 0, err
	}
	t, err := time.Parse(TimestampLayout, text)
	if err != nil || t.Year() < 1 {
		return time.Time{}, 0, dberrors.ErrType
	}
	return t, scale, nil
}

// FractionScale validates a fractional suffix and returns its precision.
func FractionScale(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	if len(text) < 2 || len(text) > 10 || text[0] != '.' {
		return 0, dberrors.ErrType
	}
	if _, err := DecimalField(text[1:], 999999999); err != nil {
		return 0, err
	}
	return len(text) - 1, nil
}

// DecimalField parses a bounded unsigned decimal field.
func DecimalField(text string, maxValue int) (int, error) {
	if len(text) == 0 || len(text) > 9 {
		return 0, dberrors.ErrType
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, dberrors.ErrType
		}
	}
	// Atoi returns int directly, so no narrowing conversion can overflow; on 32-bit
	// targets it reports ErrRange instead of wrapping.
	n, err := strconv.Atoi(text)
	if err != nil || n > maxValue {
		return 0, dberrors.ErrType
	}
	return n, nil
}

// ParseInterval validates a year/month or day/second interval without duration conversion.
func ParseInterval(text string, days bool) (IntervalParts, error) {
	var p IntervalParts
	if len(text) > 30 {
		return p, dberrors.ErrType
	}
	if strings.HasPrefix(text, "-") {
		p.Negative = true
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
		return p, dberrors.ErrType
	}
	if len(parts[0]) > 0xff {
		return p, dberrors.ErrType
	}
	p.Precision = byte(len(parts[0]))
	major, err := DecimalField(parts[0], 999999999)
	if err != nil || major < 0 || major > 999999999 {
		return p, dberrors.ErrType
	}
	p.Major = uint32(major)
	if !days {
		minor, err := DecimalField(parts[1], 11)
		if err != nil || minor < 0 || minor > 11 {
			return p, dberrors.ErrType
		}
		p.Minor = byte(minor)
		return p, nil
	}
	clock := parts[1]
	if len(clock) < 8 {
		return p, dberrors.ErrType
	}
	scale, err := FractionScale(clock[8:])
	if err != nil {
		return p, err
	}
	if scale > 0xff {
		return p, dberrors.ErrType
	}
	p.Scale = byte(scale)
	t, err := time.Parse("15:04:05", clock)
	if err != nil {
		return p, dberrors.ErrType
	}
	// time.Parse bounds these to 0-23, 0-59 and 0-59, so each fits a byte.
	p.Hour, p.Minute, p.Second = byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	p.Nanos = uint32(t.Nanosecond())
	return p, nil
}
