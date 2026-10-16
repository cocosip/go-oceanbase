package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTypeBoundaries(t *testing.T) {
	for _, text := range []string{"0", "-0.000", "12345678901234567890123456789012345678.123456789", "+.5", "1.", "1e-130", "-9.99E125", "18446744073709551615"} {
		n := Number("old")
		if err := n.Scan([]byte(text)); err != nil || string(n) != text {
			t.Fatal(text, n, err)
		}
		if v, err := n.Value(); err != nil || v != text {
			t.Fatal(v, err)
		}
		v, err := convertArgument(n)
		if err != nil || reflect.TypeOf(v) != reflect.TypeFor[Number]() {
			t.Fatal("lost numeric protocol type", v, err)
		}
	}
	for _, text := range []string{"", ".", "+", " 1", "1 ", "1e", "1e+", "NaN", "Inf", "1/2", "0x10", "1;SELECT 2", "1.2.3", "1e999999999999999999999999999999"} {
		n := Number("42")
		if err := n.Scan(text); !errors.Is(err, ErrType) || n != "42" {
			t.Fatal("invalid number changed receiver", text, n, err)
		}
		if _, err := Number(text).Value(); err == nil {
			t.Fatal("invalid number accepted by Valuer", text)
		}
	}
	var n Number
	if err := n.Scan(float64(9007199254740992)); err == nil {
		t.Fatal("cannot reconstruct an exact number from float64")
	}
	if err := n.Scan(nil); err == nil {
		t.Fatal("NULL silently became an empty number")
	}
	if err := n.Scan(int64(-9223372036854775808)); err != nil || n != "-9223372036854775808" {
		t.Fatal(n, err)
	}
	var nullable sql.Null[Number]
	if err := nullable.Scan(nil); err != nil || nullable.Valid {
		t.Fatal(nullable, err)
	}
	if err := nullable.Scan("0.000"); err != nil || !nullable.Valid || nullable.V != "0.000" {
		t.Fatal(nullable, err)
	}
}

func TestScalarByteAndTextBoundaries(t *testing.T) {
	var raw Raw
	if err := raw.Scan(nil); err != nil || raw != nil {
		t.Fatal(raw, err)
	}
	data := []byte{0, 255}
	if err := raw.Scan(data); err != nil {
		t.Fatal(err)
	}
	data[0] = 1
	if raw[0] != 0 {
		t.Fatal("RAW aliases source")
	}
	if err := raw.Scan([]byte{}); err != nil || raw == nil {
		t.Fatal("RAW empty became NULL", raw, err)
	}
	if v, err := raw.Value(); err != nil || v.([]byte) == nil {
		t.Fatal(v, err)
	}
	for _, value := range []any{NString("中文😀"), Raw{0, 255}, RowID("AAAPr9AAEAAAACXAAA")} {
		converted, err := convertArgument(value)
		if err != nil || !reflect.DeepEqual(value, converted) {
			t.Fatal("lost extension identity", value, converted, err)
		}
		if _, err = value.(driver.Valuer).Value(); err != nil {
			t.Fatal(err)
		}
	}
	var ns NString
	if err := ns.Scan(""); err != nil || ns != "" {
		t.Fatal(ns, err)
	}
	if err := ns.Scan([]byte{255}); err == nil {
		t.Fatal("invalid NCHAR UTF8 accepted")
	}
	var id RowID
	if err := id.Scan("AAAPr9AAEAAAACXAAA"); err != nil {
		t.Fatal(err)
	}
	if err := id.Scan("a' OR 1=1"); err == nil {
		t.Fatal("invalid ROWID accepted")
	}
	var np *Number
	if value, err := convertArgument(np); err != nil || value != nil {
		t.Fatal("nil extended pointer", value, err)
	}
	if err := (*Number)(nil).Scan("1"); err == nil {
		t.Fatal("nil receiver accepted")
	}
}

func TestScalarBindings(t *testing.T) {
	for _, tc := range []struct {
		value any
		mode  Mode
		typ   uint16
		data  []byte
	}{
		{Number("1.234567890123456789e2"), ModeOracle, 246, []byte("123.4567890123456789")},
		{Number("-1e-3"), ModeMySQL, 246, []byte("-0.001")},
		{Raw{0, 255}, ModeOracle, 203, []byte{0, 255}},
		{RowID("AAAABBBB"), ModeOracle, 253, []byte("AAAABBBB")},
		{NString("中文"), ModeOracle, 208, []byte{0x4e, 0x2d, 0x65, 0x87}},
	} {
		options := SessionOptions{Charset: testUTF8MB4, NCharset: testCharsetUTF16, Location: time.UTC}
		p, err := encodeParameter(tc.value, tc.mode, options, 1024)
		if err != nil || p.Type != tc.typ || !p.LengthEncoded || !reflect.DeepEqual(p.Data, tc.data) {
			t.Fatal(tc.value, p, err)
		}
	}
	for _, value := range []any{Raw{}, NString("x"), RowID("AAA")} {
		if _, err := encodeParameter(value, ModeMySQL, SessionOptions{}, 1024); !errors.Is(err, ErrUnsupported) {
			t.Fatal(value, err)
		}
	}
	c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4}, Limits: Limits{MaxLOBBytes: 1024}}, session: &handshakeSession{mode: ModeMySQL}}
	if text, err := c.clientLiteral(Number("1e-20"), 1024); err != nil || text != "0.00000000000000000001" {
		t.Fatal(text, err)
	}
	if _, err := c.clientLiteral(Number("1e2147483647"), 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("unbounded numeric expansion", err)
	}
}

func TestNullableScalarIdentity(t *testing.T) {
	values := []any{sql.Null[Number]{V: "1.25", Valid: true}, sql.Null[NString]{V: "中", Valid: true}, sql.Null[TimestampTZ]{V: "2026-01-02 03:04:05 +00:00", Valid: true}}
	want := []any{Number("1.25"), NString("中"), TimestampTZ("2026-01-02 03:04:05 +00:00")}
	for i, value := range values {
		got, err := convertArgument(value)
		if err != nil || !reflect.DeepEqual(got, want[i]) {
			t.Fatal("sql.Null lost scalar protocol identity", got, err)
		}
	}
	invalid := sql.Null[NString]{}
	if got, err := convertArgument(&invalid); err != nil || got != nil {
		t.Fatal(got, err)
	}
}
