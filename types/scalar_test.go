package types

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
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
	}
	for _, text := range []string{"", ".", "+", " 1", "1 ", "1e", "1e+", "NaN", "Inf", "1/2", "0x10", "1;SELECT 2", "1.2.3", "1e999999999999999999999999999999"} {
		n := Number("42")
		if err := n.Scan(text); !errors.Is(err, dberrors.ErrType) || n != "42" {
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
		if _, err := value.(driver.Valuer).Value(); err != nil {
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
	if err := (*Number)(nil).Scan("1"); err == nil {
		t.Fatal("nil receiver accepted")
	}
}

func TestOceanBaseRowID(t *testing.T) {
	const id = "*AAEPAQAAwAEAAAA="
	var got RowID
	if err := got.Scan(id); err != nil || got != id {
		t.Fatalf("OceanBase ROWID rejected: %v", err)
	}
	for _, invalid := range []RowID{"*", "a*b", "**AA", "*a'", "*a\x00"} {
		if _, err := invalid.Value(); err == nil {
			t.Fatal("malformed ROWID accepted")
		}
	}
}
