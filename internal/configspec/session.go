package configspec

import (
	"fmt"
	"time"
)

// SessionTimeZone returns a valid session zone name or minute-aligned fixed offset.
// The process-local zone is rejected: its meaning and offset vary by host.
func SessionTimeZone(loc *time.Location) (string, bool) {
	if loc == nil {
		loc = time.UTC
	}
	if loc == time.Local {
		return "", false
	}
	t := time.Unix(0, 0).In(loc)
	start, end := t.ZoneBounds()
	if start.IsZero() && end.IsZero() {
		_, offset := t.Zone()
		if offset%60 != 0 || offset < -12*3600 || offset > 14*3600 {
			return "", false
		}
		sign := "+"
		if offset < 0 {
			sign = "-"
			offset = -offset
		}
		return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60), true
	}
	if !ZoneToken(loc.String(), 128) {
		return "", false
	}
	return loc.String(), true
}

// SessionVariableName accepts an ASCII SQL variable identifier.
func SessionVariableName(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range []byte(name) {
		if ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

// ZoneToken accepts bounded ASCII names used in time zone identifiers.
func ZoneToken(text string, limit int) bool {
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
