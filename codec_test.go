package oceanbase

import (
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTextScalarValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column wire.Column
		mode   Mode
		data   []byte
		want   driver.Value
		bad    bool
	}{
		{"number", wire.Column{Type: 3}, ModeOracle, []byte("12345678901234567890.123456789"), "12345678901234567890.123456789", false},
		{"decimal", wire.Column{Type: 246}, ModeMySQL, []byte("0.000000000000000000000001"), "0.000000000000000000000001", false},
		{"signed", wire.Column{Type: 8}, ModeMySQL, []byte("-9223372036854775808"), int64(math.MinInt64), false},
		{"unsigned", wire.Column{Type: 8, Flags: 32}, ModeMySQL, []byte("18446744073709551615"), "18446744073709551615", false},
		{"unsigned-small", wire.Column{Type: 8, Flags: 32}, ModeMySQL, []byte("42"), int64(42), false},
		{"raw", wire.Column{Type: 203}, ModeOracle, []byte{0, 255}, []byte{0, 255}, false},
		{"float", wire.Column{Type: 5}, ModeOracle, []byte("-1.25"), float64(-1.25), false},
		{"invalid-float", wire.Column{Type: 4}, ModeOracle, []byte("abc"), nil, true},
		{"overflow-float", wire.Column{Type: 4}, ModeOracle, []byte("1e40"), nil, true},
		{"mysql-special-float", wire.Column{Type: 5}, ModeMySQL, []byte("INF"), nil, true},
		{"invalid-integer", wire.Column{Type: 8}, ModeMySQL, []byte("9223372036854775808"), nil, true},
		{"invalid-unsigned", wire.Column{Type: 8, Flags: 32}, ModeMySQL, []byte("-1"), nil, true},
		{"invalid-utf8", wire.Column{Type: 253, Charset: 45}, ModeMySQL, []byte{255}, nil, true},
		{"null", wire.Column{Type: 3}, ModeOracle, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeTextValue(tc.column, tc.mode, tc.data)
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
	_, err := decodeTextValue(wire.Column{Type: 200}, ModeMySQL, []byte("x"))
	var unsupported *UnsupportedTypeError
	if !errors.Is(err, ErrUnsupported) || !errors.As(err, &unsupported) || unsupported.Type != 200 {
		t.Fatal(err)
	}
}

func TestTextScanTypes(t *testing.T) {
	for _, tc := range []struct {
		typ     byte
		charset uint16
		want    reflect.Type
	}{
		{245, 63, reflect.TypeFor[string]()},
		{253, 63, reflect.TypeFor[[]byte]()},
		{254, 63, reflect.TypeFor[[]byte]()},
		{252, 45, reflect.TypeFor[string]()},
		{3, 63, reflect.TypeFor[int64]()},
	} {
		r := textRows{conn: &conn{session: &handshakeSession{mode: ModeMySQL}}, columns: []wire.Column{{Type: tc.typ, Charset: tc.charset}}}
		if got := r.ColumnTypeScanType(0); got != tc.want {
			t.Errorf("type=%d charset=%d: got %v, want %v", tc.typ, tc.charset, got, tc.want)
		}
	}
}

func TestResultUnknownAndOverflow(t *testing.T) {
	if _, err := (result{}).RowsAffected(); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := (result{}).LastInsertId(); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := (result{known: true, id: math.MaxUint64}).LastInsertId(); !errors.Is(err, errCountOverflow) {
		t.Fatal(err)
	}
}
