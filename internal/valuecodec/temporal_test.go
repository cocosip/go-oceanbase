package valuecodec

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

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
			options := config.SessionOptions{Location: time.FixedZone("", 8*3600)}
			v, err := DecodeRowValue(wire.Column{Type: tc.typ}, binary, tc.data, config.ModeOracle, options)
			if err != nil || v != tc.want {
				t.Fatal(binary, tc.typ, v, err)
			}
			if !driver.IsValue(v) {
				t.Fatal("non-standard scan source", v)
			}
		}
	}
	options := config.SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}
	value := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	p, err := EncodeParameter(value, config.ModeOracle, options, 1024)
	if err != nil || p.Type != 202 || !p.LengthEncoded || !reflect.DeepEqual(p.Data, base) {
		t.Fatal(p, err)
	}
	for _, tc := range []struct {
		value any
		typ   uint16
		data  []byte
	}{
		{types.IntervalYM("0-00"), 204, []byte{0, 0, 0, 0, 0, 0, 1}},
		{types.IntervalDS("0 00:00:00"), 205, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0}},
		{types.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), 200, append(append([]byte{}, base...), 0, 226, 0, 0)},
		{types.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), 201, base},
		{types.IntervalYM("-999999999-11"), 204, []byte{1, 255, 201, 154, 59, 11, 9}},
		{types.IntervalDS("-999999999 23:59:59.123456789"), 205, []byte{1, 255, 201, 154, 59, 23, 59, 59, 0x15, 0xcd, 0x5b, 7, 9, 9}},
	} {
		p, err = EncodeParameter(tc.value, config.ModeOracle, options, 1024)
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
		options := config.SessionOptions{}
		if _, err = DecodeRowValue(wire.Column{Type: tc.typ}, false, tc.data, config.ModeOracle, options); err == nil {
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
	options := config.SessionOptions{Location: loc}
	value, err := DecodeRowValue(wire.Column{Type: 201}, false, data, config.ModeOracle, options)
	want := "2024-11-03 01:30:00.000000001 America/New_York"
	if err != nil || value != want {
		t.Fatal("LTZ invented an offset during a DST overlap", value, err)
	}
	var local types.TimestampLTZ
	if err = local.Scan(value); err != nil {
		t.Fatal(err)
	}
	if _, err = local.Time(); !errors.Is(err, dberrors.ErrType) {
		t.Fatal("ambiguous wall time became a fabricated instant", err)
	}
	p, err := EncodeParameter(local, config.ModeOracle, options, 1024)
	if err != nil || !reflect.DeepEqual(p.Data, data) {
		t.Fatal(p, err)
	}
	if _, err = EncodeParameter(local, config.ModeOracle, config.SessionOptions{Location: time.UTC}, 1024); !errors.Is(err, dberrors.ErrType) {
		t.Fatal("ambiguous LTZ changed zone", err)
	}
}

func TestTemporalYearOneDecode(t *testing.T) {
	got, err := DecodeRowValue(wire.Column{Type: 202}, false, []byte{0, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0}, config.ModeOracle, config.SessionOptions{ParseTime: true, Location: time.UTC})
	if err != nil || got != (time.Time{}) {
		t.Fatal(got, err)
	}
}
