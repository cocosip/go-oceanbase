package valueformat

import (
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/dberrors"
)

const (
	newYorkZone = "America/New_York"
	sampleWall  = "2026-11-04 10:20:30"
)

func TestParseWallAndFraction(t *testing.T) {
	wall, scale, err := ParseWall(sampleWall)
	if err != nil || scale != 0 || wall.Format(TimestampLayout) != sampleWall {
		t.Fatalf("whole seconds: %v scale=%d err=%v", wall, scale, err)
	}
	wall, scale, err = ParseWall("2026-11-04 10:20:30.123456789")
	if err != nil || scale != 9 || wall.Nanosecond() != 123456789 {
		t.Fatalf("nanoseconds: %v scale=%d err=%v", wall, scale, err)
	}
	for _, text := range []string{
		"2026-11-04 10:20:30.",
		"2026-11-04 10:20:30.1234567890",
		"2026-11-04 10:20:30x",
		"0000-01-01 00:00:00",
		"2026-13-01 00:00:00",
		"2026-11-04T10:20:30",
	} {
		if _, _, err := ParseWall(text); !errors.Is(err, dberrors.ErrType) {
			t.Errorf("ParseWall(%q) = %v, want ErrType", text, err)
		}
	}
}

func TestFractionScaleAndDecimalField(t *testing.T) {
	for _, tc := range []struct {
		text  string
		scale int
		ok    bool
	}{
		{"", 0, true},
		{".1", 1, true},
		{".123456789", 9, true},
		{".1234567890", 0, false},
		{"1", 0, false},
		{".", 0, false},
		{".12a", 0, false},
	} {
		scale, err := FractionScale(tc.text)
		if tc.ok && (err != nil || scale != tc.scale) {
			t.Errorf("FractionScale(%q) = %d, %v", tc.text, scale, err)
		}
		if !tc.ok && !errors.Is(err, dberrors.ErrType) {
			t.Errorf("FractionScale(%q) err = %v, want ErrType", tc.text, err)
		}
	}
	for _, tc := range []struct {
		text string
		max  int
		want int
		ok   bool
	}{
		{"0", 0, 0, true},
		{"42", 42, 42, true},
		{"42", 41, 0, false},
		{"", 9, 0, false},
		{"1234567890", 999999999, 0, false},
		{"-1", 9, 0, false},
		{"1a", 9, 0, false},
	} {
		got, err := DecimalField(tc.text, tc.max)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("DecimalField(%q, %d) = %d, %v", tc.text, tc.max, got, err)
		}
		if !tc.ok && !errors.Is(err, dberrors.ErrType) {
			t.Errorf("DecimalField(%q, %d) err = %v, want ErrType", tc.text, tc.max, err)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	p, err := ParseTimestamp("2026-11-04 10:20:30.5 +08:00")
	if err != nil || p.Offset != 8*3600 || p.Scale != 1 {
		t.Fatalf("offset parts: %+v err=%v", p, err)
	}
	if _, offset := p.Instant.Zone(); offset != 8*3600 || p.Instant.Nanosecond() != 500000000 {
		t.Fatalf("instant zone: %v", p.Instant)
	}
	p, err = ParseTimestamp("2026-11-04 10:20:30 -05:00 " + newYorkZone + " EST")
	if err != nil || p.Region != "America/New_York" || p.Abbr != "EST" || p.Offset != -5*3600 {
		t.Fatalf("region parts: %+v err=%v", p, err)
	}
	for _, text := range []string{
		sampleWall,
		"2026-11-04 10:20:30 +08:00 extra more parts",
		"2026-11-04 10:20:30 08:00",
		"2026-11-04 10:20:30 +8:00",
		"2026-11-04 10:20:30 +08:60",
		"2026-11-04 10:20:30 +15:00",
		"2026-11-04 10:20:30 -05:00 Asia/Shanghai",
		"2026-11-04 10:20:30 +08:00 Bad@Zone",
	} {
		if _, err := ParseTimestamp(text); !errors.Is(err, dberrors.ErrType) {
			t.Errorf("ParseTimestamp(%q) err = %v, want ErrType", text, err)
		}
	}
	long := "2026-11-04 10:20:30 +08:00 " + string(make([]byte, 240))
	if _, err := ParseTimestamp(long); !errors.Is(err, dberrors.ErrType) {
		t.Errorf("oversized timestamp err = %v, want ErrType", err)
	}
}

func TestParseLTZ(t *testing.T) {
	p, err := ParseLTZ("2026-11-04 10:20:30 " + newYorkZone)
	if err != nil || !p.Local || p.Region != "America/New_York" || p.Scale != 0 {
		t.Fatalf("region LTZ: %+v err=%v", p, err)
	}
	p, err = ParseLTZ("2026-11-04 10:20:30 +09:00")
	if err != nil || p.Local || p.Offset != 9*3600 {
		t.Fatalf("offset LTZ fallback: %+v err=%v", p, err)
	}
	for _, text := range []string{
		sampleWall,
		"2026-11-04 10:20:30 Bad@Zone",
	} {
		if _, err := ParseLTZ(text); !errors.Is(err, dberrors.ErrType) {
			t.Errorf("ParseLTZ(%q) err = %v, want ErrType", text, err)
		}
	}
}

func TestResolveLocal(t *testing.T) {
	loc, err := time.LoadLocation(newYorkZone)
	if err != nil {
		t.Skipf("time zone database unavailable: %v", err)
	}
	wall, _, err := ParseWall("2026-06-01 12:00:00")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLocal(wall, loc)
	if err != nil || got.UTC().Hour() != 16 {
		t.Fatalf("unambiguous resolve: %v err=%v", got, err)
	}
	// 2026-11-01 01:30 occurs twice in America/New_York.
	fold, _, err := ParseWall("2026-11-01 01:30:00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocal(fold, loc); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("ambiguous wall resolved: err=%v", err)
	}
	// 2026-03-08 02:30 does not exist in America/New_York.
	gap, _, err := ParseWall("2026-03-08 02:30:00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLocal(gap, loc); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("missing wall resolved: err=%v", err)
	}
}

func TestParseInterval(t *testing.T) {
	p, err := ParseInterval("-5-3", false)
	if err != nil || !p.Negative || p.Major != 5 || p.Minor != 3 || p.Precision != 1 {
		t.Fatalf("year-month: %+v err=%v", p, err)
	}
	p, err = ParseInterval("+12-11", false)
	if err != nil || p.Negative || p.Major != 12 || p.Minor != 11 || p.Precision != 2 {
		t.Fatalf("year-month positive: %+v err=%v", p, err)
	}
	p, err = ParseInterval("-5 10:20:30.123", true)
	if err != nil || !p.Negative || p.Major != 5 || p.Hour != 10 || p.Minute != 20 || p.Second != 30 {
		t.Fatalf("day-second: %+v err=%v", p, err)
	}
	if p.Nanos != 123000000 || p.Scale != 3 {
		t.Fatalf("day-second fraction: %+v", p)
	}
	for _, tc := range []struct {
		text string
		days bool
	}{
		{"5-3", true},
		{"5 3", false},
		{"5-12", false},
		{"5 10:20", true},
		{"5 10:60:00", true},
		{"5 10:20:30.1234567890", true},
		{"-5-3-1", false},
		{"1234567890-1", false},
	} {
		if _, err := ParseInterval(tc.text, tc.days); !errors.Is(err, dberrors.ErrType) {
			t.Errorf("ParseInterval(%q, %v) err = %v, want ErrType", tc.text, tc.days, err)
		}
	}
}

func TestScalarText(t *testing.T) {
	if got, err := ScalarText("plain"); err != nil || got != "plain" {
		t.Fatalf("string: %q %v", got, err)
	}
	if got, err := ScalarText([]byte("bytes")); err != nil || got != "bytes" {
		t.Fatalf("bytes: %q %v", got, err)
	}
	if _, err := ScalarText([]byte(nil)); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("nil bytes err = %v", err)
	}
	if _, err := ScalarText([]byte{0xff, 0xfe}); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("invalid UTF-8 err = %v", err)
	}
	if _, err := ScalarText(42); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("unsupported type err = %v", err)
	}
}

func TestIdentifier(t *testing.T) {
	for _, name := range []string{"T", "schema.table", "汉字", "a b"} {
		if !Identifier(name) {
			t.Errorf("Identifier(%q) = false", name)
		}
	}
	for _, name := range []string{"", "a\x00b", string([]byte{0xff})} {
		if Identifier(name) {
			t.Errorf("Identifier(%q) = true", name)
		}
	}
}

func TestComplexNode(t *testing.T) {
	nodes := MaxComplexNodes
	if err := ComplexNode(0, &nodes); err != nil || nodes != MaxComplexNodes-1 {
		t.Fatalf("first node: %v nodes=%d", err, nodes)
	}
	if err := ComplexNode(MaxComplexDepth+1, &nodes); !errors.Is(err, dberrors.ErrComplex) {
		t.Fatalf("depth limit err = %v", err)
	}
	empty := 0
	if err := ComplexNode(0, &empty); !errors.Is(err, dberrors.ErrComplex) {
		t.Fatalf("node limit err = %v", err)
	}
}
