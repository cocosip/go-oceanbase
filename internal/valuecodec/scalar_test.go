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
