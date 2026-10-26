package valuecodec

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestTemporalParsedPrecision(t *testing.T) {
	for _, scale := range []byte{0, 3, 6, 9} {
		nanos := uint32(0)
		if scale > 0 {
			nanos = 123000000
		}
		if scale >= 6 {
			nanos = 123456000
		}
		if scale == 9 {
			nanos = 123456789
		}
		for _, typ := range []byte{200, 201, 202} {
			data := []byte{20, 24, 2, 29, 23, 59, 59}
			data = binary.LittleEndian.AppendUint32(data, nanos)
			data = append(data, scale)
			if typ == 200 {
				data = append(data, 0, 0, 0, 0)
			}
			for _, binaryRows := range []bool{false, true} {
				value, err := DecodeRowValue(wire.Column{Type: typ}, binaryRows, data, config.ModeOracle, config.SessionOptions{ParseTime: true})
				instant, ok := value.(time.Time)
				if err != nil || !ok || instant.Nanosecond() != int(nanos) || instant.Day() != 29 || instant.Second() != 59 {
					t.Fatalf("type %d scale %d: %v %v", typ, scale, value, err)
				}
			}
		}
	}
}

func TestMySQLParsedInvalidDates(t *testing.T) {
	for _, tc := range []struct {
		text string
		data []byte
	}{
		{"0000-00-00 00:00:00", nil},
		{"2024-00-01 12:34:56", []byte{0xe8, 7, 0, 1, 12, 34, 56}},
		{"2024-02-30 12:34:56", []byte{0xe8, 7, 2, 30, 12, 34, 56}},
	} {
		for _, typ := range []byte{7, 12} {
			for _, binaryRows := range []bool{false, true} {
				data := []byte(tc.text)
				if binaryRows {
					data = append([]byte{}, tc.data...)
				}
				options := config.SessionOptions{ParseTime: true, Location: time.UTC}
				if _, err := DecodeRowValue(wire.Column{Type: typ}, binaryRows, data, config.ModeMySQL, options); !errors.Is(err, dberrors.ErrType) {
					t.Fatalf("invalid date accepted: %q %v", tc.text, err)
				}
				options.ParseTime = false
				if value, err := DecodeRowValue(wire.Column{Type: typ}, binaryRows, data, config.ModeMySQL, options); err != nil || value != tc.text {
					t.Fatalf("text preservation changed: %v %v", value, err)
				}
			}
		}
	}
}

func TestOracleZonedParseTime(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		value    any
		location *time.Location
		want     time.Time
	}{
		{"tz-offset", types.TimestampTZ("2024-02-29 12:34:56.123456789 -00:30"), time.UTC, time.Date(2024, 2, 29, 12, 34, 56, 123456789, time.FixedZone("", -1800))},
		{"tz-region", types.TimestampTZ("2024-11-03 01:30:00.000000001 -04:00 America/New_York EDT"), time.UTC, time.Date(2024, 11, 3, 5, 30, 0, 1, time.UTC).In(newYork)},
		{"tz-unknown-region", types.TimestampTZ("2024-02-29 12:34:56 +08:00 Unknown/Region"), time.UTC, time.Date(2024, 2, 29, 12, 34, 56, 0, time.FixedZone("", 8*3600))},
		{"ltz-utc", types.TimestampLTZ("2024-02-29 12:34:56.123456789 +00:00"), nil, time.Date(2024, 2, 29, 12, 34, 56, 123456789, time.UTC)},
		{"ltz-misleading-label", types.TimestampLTZ("2024-02-29 12:34:56 +00:00"), time.FixedZone("America/New_York", 0), time.Date(2024, 2, 29, 12, 34, 56, 0, time.FixedZone("America/New_York", 0))},
		{"ltz-region", types.TimestampLTZ("2024-07-01 12:34:56.123456789 America/New_York"), newYork, time.Date(2024, 7, 1, 12, 34, 56, 123456789, newYork)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := config.SessionOptions{Location: tc.location}
			parameter, err := EncodeParameter(tc.value, config.ModeOracle, options, 1024)
			if err != nil {
				t.Fatal(err)
			}
			column := wire.Column{Type: byte(parameter.Type)}
			for _, binaryRows := range []bool{false, true} {
				text, err := DecodeRowValue(column, binaryRows, parameter.Data, config.ModeOracle, options)
				if err != nil || reflect.TypeOf(text) != reflect.TypeFor[string]() {
					t.Fatal(text, err)
				}
				options.ParseTime = true
				got, err := DecodeRowValue(column, binaryRows, parameter.Data, config.ModeOracle, options)
				instant, ok := got.(time.Time)
				if err != nil || !ok || !instant.Equal(tc.want) || instant.Format("-07:00") != tc.want.Format("-07:00") {
					t.Fatalf("got %v (%v), want %v", got, err, tc.want)
				}
				if ColumnScanType(column, config.ModeOracle, options) != reflect.TypeFor[time.Time]() {
					t.Fatal("incorrect scan type")
				}
				if column.Type == 201 && tc.location != nil && instant.Location() != tc.location {
					t.Fatal("LTZ session location lost")
				}
				if got, err := DecodeRowValue(column, binaryRows, nil, config.ModeOracle, options); got != nil || err != nil {
					t.Fatal("NULL changed", got, err)
				}
				options.ParseTime = false
			}
		})
	}
}

func TestOracleLTZParseTimeRejectsAmbiguity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range [][4]byte{{3, 10, 2, 30}, {11, 3, 1, 30}} {
		data := []byte{20, 24, date[0], date[1], date[2], date[3], 0, 0, 0, 0, 0, 0}
		for _, binaryRows := range []bool{false, true} {
			options := config.SessionOptions{Location: loc, ParseTime: true}
			if _, err := DecodeRowValue(wire.Column{Type: 201}, binaryRows, data, config.ModeOracle, options); !errors.Is(err, dberrors.ErrType) {
				t.Fatalf("ambiguous/invalid wall time accepted: %v", err)
			}
			options.ParseTime = false
			if _, err := DecodeRowValue(wire.Column{Type: 201}, binaryRows, data, config.ModeOracle, options); err != nil {
				t.Fatal("text preservation failed", err)
			}
		}
	}
}
