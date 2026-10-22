package valuecodec

import (
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const maxUint64Literal = "18446744073709551615"

func TestTextScalarValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column wire.Column
		mode   config.Mode
		data   []byte
		want   driver.Value
		bad    bool
	}{
		{"number", wire.Column{Type: 3}, config.ModeOracle, []byte("12345678901234567890.123456789"), "12345678901234567890.123456789", false},
		{"decimal", wire.Column{Type: 246}, config.ModeMySQL, []byte("0.000000000000000000000001"), "0.000000000000000000000001", false},
		{"signed", wire.Column{Type: 8}, config.ModeMySQL, []byte("-9223372036854775808"), int64(math.MinInt64), false},
		{"unsigned", wire.Column{Type: 8, Flags: 32}, config.ModeMySQL, []byte(maxUint64Literal), maxUint64Literal, false},
		{"unsigned-small", wire.Column{Type: 8, Flags: 32}, config.ModeMySQL, []byte("42"), int64(42), false},
		{"raw", wire.Column{Type: 203}, config.ModeOracle, []byte{0, 255}, []byte{0, 255}, false},
		{"float", wire.Column{Type: 5}, config.ModeOracle, []byte("-1.25"), float64(-1.25), false},
		{"invalid-float", wire.Column{Type: 4}, config.ModeOracle, []byte("abc"), nil, true},
		{"overflow-float", wire.Column{Type: 4}, config.ModeOracle, []byte("1e40"), nil, true},
		{"mysql-special-float", wire.Column{Type: 5}, config.ModeMySQL, []byte("INF"), nil, true},
		{"invalid-integer", wire.Column{Type: 8}, config.ModeMySQL, []byte("9223372036854775808"), nil, true},
		{"invalid-unsigned", wire.Column{Type: 8, Flags: 32}, config.ModeMySQL, []byte("-1"), nil, true},
		{"invalid-utf8", wire.Column{Type: 253, Charset: 45}, config.ModeMySQL, []byte{255}, nil, true},
		{"null", wire.Column{Type: 3}, config.ModeOracle, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeTextValue(tc.column, tc.mode, tc.data)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid value accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v (%v), want %#v", got, err, tc.want)
			}
			if len(tc.data) > 0 {
				tc.data[0] ^= 255
				if b, ok := got.([]byte); ok && reflect.DeepEqual(b, tc.data) {
					t.Fatal("decoded bytes alias packet")
				}
			}
		})
	}
	_, err := DecodeTextValue(wire.Column{Type: 200}, config.ModeMySQL, []byte("x"))
	var unsupported *dberrors.UnsupportedTypeError
	if !errors.Is(err, dberrors.ErrUnsupported) || !errors.As(err, &unsupported) || unsupported.Type != 200 {
		t.Fatal(err)
	}
}

func TestMySQLCompatibilityProfiles(t *testing.T) {
	const timeFraction = "12:34:56.1000"
	textColumn := wire.Column{Type: 250, Charset: 45, NewMetadata: true}
	if got := TextTypeName(textColumn, config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 5}); got != "VARCHAR" {
		t.Fatalf("MySQL 5 text metadata: %q", got)
	}
	if got := TextTypeName(textColumn, config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 8}); got != "TEXT" {
		t.Fatalf("MySQL 8 text metadata: %q", got)
	}
	textColumn.NewMetadata = false
	if got := TextTypeName(textColumn, config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 8}); got != "VARCHAR" {
		t.Fatalf("old MySQL 8 text metadata: %q", got)
	}
	for _, tc := range []struct {
		version int
		data    string
		want    string
	}{
		{5, timeFraction, "12:34:56"},
		{8, timeFraction, timeFraction},
	} {
		got, err := DecodeTextValue(wire.Column{Type: 11}, config.ModeMySQL, []byte(tc.data), config.SessionOptions{CompatibleMysqlVersion: tc.version})
		if err != nil || got != tc.want {
			t.Fatalf("TIME version %d: got %#v (%v), want %q", tc.version, got, err, tc.want)
		}
	}
	got, err := DecodeTextValue(wire.Column{Type: 12}, config.ModeMySQL, []byte("2024-01-01 12:00:00.0000"), config.SessionOptions{CompatibleMysqlVersion: 8})
	if err != nil || got != "2024-01-01 12:00:00" {
		t.Fatalf("MySQL 8 zero fraction normalization: got %#v (%v)", got, err)
	}
	got, err = DecodeBinaryValue(wire.Column{Type: 12}, config.ModeMySQL, []byte{0xe8, 0x07, 1, 1, 12, 0, 0, 0, 0, 0, 0}, config.SessionOptions{CompatibleMysqlVersion: 8})
	if err != nil || got != "2024-01-01 12:00:00" {
		t.Fatalf("MySQL 8 binary temporal normalization: got %#v (%v)", got, err)
	}
	got, err = DecodeBinaryValue(wire.Column{Type: 12, Decimals: 4}, config.ModeMySQL, []byte{0xe8, 0x07, 1, 1, 12, 0, 0, 0x10, 0x27, 0, 0}, config.SessionOptions{CompatibleMysqlVersion: 8})
	if err != nil || got != "2024-01-01 12:00:00.0100" {
		t.Fatalf("MySQL 8 binary temporal precision: got %#v (%v)", got, err)
	}
	got, err = DecodeTextValue(wire.Column{Type: 12}, config.ModeOracle, []byte("2024-01-01 12:00:00.0000"), config.SessionOptions{CompatibleMysqlVersion: 8})
	if err != nil || got != "2024-01-01 12:00:00.0000" {
		t.Fatalf("Oracle temporal value changed by MySQL profile: got %#v (%v)", got, err)
	}
	if _, err = DecodeRowValue(wire.Column{Type: 10}, false, []byte("0000-00-00"), config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 5, ParseTime: true}); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("MySQL 5 zero date should remain unrepresentable: %v", err)
	}
	if _, err = DecodeRowValue(wire.Column{Type: 10}, false, []byte("0000-00-00"), config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 8, ParseTime: true}); !errors.Is(err, dberrors.ErrType) {
		t.Fatalf("MySQL 8 zero date should preserve Go ParseTime semantics: %v", err)
	}
	for _, version := range []int{5, 8} {
		value, err := DecodeTextValue(wire.Column{Type: 13}, config.ModeMySQL, []byte("0000"), config.SessionOptions{CompatibleMysqlVersion: version})
		if err != nil || value != int64(0) {
			t.Fatalf("YEAR version %d changed Go integer semantics: %#v (%v)", version, value, err)
		}
	}
}

func TestBinaryFloatModes(t *testing.T) {
	data := binary.LittleEndian.AppendUint64(nil, math.Float64bits(math.Inf(1)))
	if _, err := DecodeBinaryValue(wire.Column{Type: 5}, config.ModeMySQL, data); err == nil {
		t.Fatal("MySQL nonfinite accepted")
	}
	v, err := DecodeBinaryValue(wire.Column{Type: 5}, config.ModeOracle, data)
	if err != nil || !math.IsInf(v.(float64), 1) {
		t.Fatal(v, err)
	}
}

func TestBinaryTimeAndScalars(t *testing.T) {
	for _, tc := range []struct {
		typ  byte
		data []byte
		want any
	}{
		{1, []byte{255}, int64(-1)}, {2, []byte{0, 128}, int64(-32768)},
		{3, []byte{255, 255, 255, 127}, int64(2147483647)},
		{10, []byte{}, "0000-00-00"}, {12, []byte{}, "0000-00-00 00:00:00"},
		{10, []byte{233, 7, 10, 17}, "2025-10-17"},
		{12, []byte{233, 7, 10, 17, 12, 34, 56, 64, 226, 1, 0}, "2025-10-17 12:34:56.123456"},
		{11, []byte{1, 2, 0, 0, 0, 3, 4, 5, 1, 0, 0, 0}, "-51:04:05.000001"},
	} {
		v, err := DecodeBinaryValue(wire.Column{Type: tc.typ}, config.ModeMySQL, tc.data)
		if err != nil || !reflect.DeepEqual(v, tc.want) {
			t.Fatalf("type=%d got %v want %v: %v", tc.typ, v, tc.want, err)
		}
	}
	for _, tc := range []struct {
		typ  byte
		data []byte
	}{
		{10, []byte{1}}, {12, []byte{233, 7, 1, 1, 24, 0, 0}},
		{11, []byte{2, 0, 0, 0, 0, 0, 0, 0}}, {12, []byte{233, 7, 1, 1, 0, 0, 0, 64, 66, 15, 0}},
	} {
		if _, err := DecodeBinaryValue(wire.Column{Type: tc.typ}, config.ModeMySQL, tc.data); err == nil {
			t.Fatal("invalid temporal accepted", tc)
		}
	}
	local := time.FixedZone("test", 8*3600)
	value := time.Date(2025, 10, 17, 4, 34, 56, 123456000, time.UTC)
	p, err := EncodeParameter(value, config.ModeMySQL, config.SessionOptions{Location: local}, 1024)
	want := []byte{11, 233, 7, 10, 17, 12, 34, 56, 64, 226, 1, 0}
	if err != nil || p.Type != 12 || !reflect.DeepEqual(p.Data, want) {
		t.Fatal(p, err)
	}
	if _, err = EncodeParameter(value.Add(time.Nanosecond), config.ModeMySQL, config.SessionOptions{Location: local}, 1024); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal("unrepresentable precision silently truncated", err)
	}
	if _, err = EncodeParameter("oversized", config.ModeMySQL, config.SessionOptions{Location: local}, 3); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}
