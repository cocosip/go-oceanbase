package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTemporalLossless(t *testing.T) {
	for _, tc := range []struct {
		scanner sql.Scanner
		value   driver.Valuer
		text    string
	}{
		{new(TimestampTZ), TimestampTZ("2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT"), "2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT"},
		{new(TimestampLTZ), TimestampLTZ("2024-11-03 01:30:00.000000001 -05:00 America/New_York"), "2024-11-03 01:30:00.000000001 -05:00 America/New_York"},
		{new(IntervalYM), IntervalYM("-999999999-11"), "-999999999-11"},
		{new(IntervalDS), IntervalDS("-999999999 23:59:59.123456789"), "-999999999 23:59:59.123456789"},
	} {
		if err := tc.scanner.Scan([]byte(tc.text)); err != nil {
			t.Fatal(tc.text, err)
		}
		v, err := tc.scanner.(driver.Valuer).Value()
		if err != nil || v != tc.text {
			t.Fatal(tc.text, v, err)
		}
		if v, err = convertArgument(tc.value); err != nil || reflect.TypeOf(v) != reflect.TypeOf(tc.value) {
			t.Fatal("lost temporal identity", v, err)
		}
		if err = tc.scanner.Scan(nil); !errors.Is(err, ErrType) {
			t.Fatal(err)
		}
	}
	for _, value := range []driver.Valuer{
		TimestampTZ("2024-02-30 00:00:00 +00:00"), TimestampTZ("2024-01-01 00:00:00.1234567890 +00:00"),
		TimestampTZ("2024-01-01 00:00:00 +14:01"), TimestampTZ("2024-03-10 02:30:00 -05:00 America/New_York"),
		TimestampTZ("2024-01-01 00:00:00 +00:00 x';select"), TimestampLTZ("2024-01-01 00:00:00"),
		IntervalYM("1000000000-0"), IntervalYM("1-12"), IntervalYM("1--1"), IntervalYM("1-256"),
		IntervalYM("1-999999999"), IntervalYM("4294967296-0"), IntervalDS("1 24:00:00"),
		IntervalDS("1 00:00:00.1234567890"), IntervalDS("1000000000 00:00:00"),
		IntervalDS("4294967296 00:00:00"),
	} {
		if _, err := value.Value(); !errors.Is(err, ErrType) {
			t.Fatal("invalid temporal accepted", value, err)
		}
	}
	first, err := TimestampTZ("2024-11-03 01:30:00 -04:00 America/New_York").Time()
	if err != nil {
		t.Fatal(err)
	}
	second, err := TimestampTZ("2024-11-03 01:30:00 -05:00 America/New_York").Time()
	if err != nil || second.Sub(first) != time.Hour {
		t.Fatal("DST overlap lost", first, second, err)
	}
}

func TestTemporalWire(t *testing.T) {
	base := []byte{20, 26, 1, 2, 3, 4, 5, 0x15, 0xcd, 0x5b, 7, 9}
	for _, binary := range []bool{false, true} {
		for _, tc := range []struct {
			typ  byte
			data []byte
			want string
		}{
			{202, base, "2026-01-02 03:04:05.123456789"},
			{201, base, "2026-01-02 03:04:05.123456789 +08:00"},
			{200, append(append([]byte{}, base...), 0, 226, 0, 0), "2026-01-02 03:04:05.123456789 -00:30"},
			{204, []byte{1, 255, 201, 154, 59, 11, 9}, "-999999999-11"},
			{204, []byte{0, 0, 0, 0, 0, 1, 0}, "0-01"},
			{205, []byte{0, 0, 0, 0, 0, 1, 2, 3, 0, 0, 0, 0, 0, 0}, "0 01:02:03"},
			{205, []byte{1, 255, 201, 154, 59, 23, 59, 59, 0x15, 0xcd, 0x5b, 7, 9, 9}, "-999999999 23:59:59.123456789"},
		} {
			c := &conn{cfg: Config{Session: SessionOptions{Location: time.FixedZone("", 8*3600)}}, session: &handshakeSession{mode: ModeOracle}}
			v, err := c.decodeRowValue(wire.Column{Type: tc.typ}, binary, tc.data)
			if err != nil || v != tc.want {
				t.Fatal(binary, tc.typ, v, err)
			}
			if !driver.IsValue(v) {
				t.Fatal("non-standard scan source", v)
			}
		}
	}
	options := SessionOptions{Charset: testUTF8MB4, Location: time.UTC}
	value := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	p, err := encodeParameter(value, ModeOracle, options, 1024)
	if err != nil || p.Type != 202 || !p.LengthEncoded || !reflect.DeepEqual(p.Data, base) {
		t.Fatal(p, err)
	}
	for _, tc := range []struct {
		value any
		typ   uint16
		data  []byte
	}{
		{IntervalYM("0-00"), 204, []byte{0, 0, 0, 0, 0, 0, 1}},
		{IntervalDS("0 00:00:00"), 205, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0}},
		{TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), 200, append(append([]byte{}, base...), 0, 226, 0, 0)},
		{TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), 201, base},
		{IntervalYM("-999999999-11"), 204, []byte{1, 255, 201, 154, 59, 11, 9}},
		{IntervalDS("-999999999 23:59:59.123456789"), 205, []byte{1, 255, 201, 154, 59, 23, 59, 59, 0x15, 0xcd, 0x5b, 7, 9, 9}},
	} {
		p, err = encodeParameter(tc.value, ModeOracle, options, 1024)
		if err != nil || p.Type != tc.typ || !p.LengthEncoded || !reflect.DeepEqual(p.Data, tc.data) {
			t.Fatal(tc.value, p, err)
		}
	}
	for _, tc := range []struct {
		typ  byte
		data []byte
	}{
		{202, []byte{20}}, {202, []byte{20, 26, 2, 30, 0, 0, 0, 0, 0, 0, 0, 0}},
		{202, []byte{20, 26, 1, 2, 0, 0, 0, 1, 0, 0, 0, 0}}, {204, []byte{0, 1, 0, 0, 0, 12, 1}},
		{205, []byte{0, 1, 0, 0, 0, 24, 0, 0, 0, 0, 0, 0, 1, 0}},
	} {
		c := &conn{session: &handshakeSession{mode: ModeOracle}}
		if _, err = c.decodeRowValue(wire.Column{Type: tc.typ}, false, tc.data); err == nil {
			t.Fatal("invalid temporal packet", tc)
		}
	}
}

func TestTimestampLTZAmbiguity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{20, 24, 11, 3, 1, 30, 0, 1, 0, 0, 0, 9}
	c := &conn{cfg: Config{Session: SessionOptions{Location: loc}}, session: &handshakeSession{mode: ModeOracle}}
	value, err := c.decodeRowValue(wire.Column{Type: 201}, false, data)
	want := "2024-11-03 01:30:00.000000001 America/New_York"
	if err != nil || value != want {
		t.Fatal("LTZ invented an offset during a DST overlap", value, err)
	}
	var local TimestampLTZ
	if err = local.Scan(value); err != nil {
		t.Fatal(err)
	}
	if _, err = local.Time(); !errors.Is(err, ErrType) {
		t.Fatal("ambiguous wall time became a fabricated instant", err)
	}
	p, err := encodeParameter(local, ModeOracle, c.cfg.Session, 1024)
	if err != nil || !reflect.DeepEqual(p.Data, data) {
		t.Fatal(p, err)
	}
	if _, err = encodeParameter(local, ModeOracle, SessionOptions{Location: time.UTC}, 1024); !errors.Is(err, ErrType) {
		t.Fatal("ambiguous LTZ changed zone", err)
	}
}

func TestTemporalYearOne(t *testing.T) {
	value, err := TimestampLTZ("0001-01-01 00:00:00 UTC").Time()
	if err != nil || !value.IsZero() {
		t.Fatal("valid zero time rejected", value, err)
	}
	c := &conn{cfg: Config{Session: SessionOptions{ParseTime: true, Location: time.UTC}}, session: &handshakeSession{mode: ModeOracle}}
	got, err := c.decodeRowValue(wire.Column{Type: 202}, false, []byte{0, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0})
	if err != nil || got != (time.Time{}) {
		t.Fatal(got, err)
	}
}
