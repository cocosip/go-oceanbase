package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestComplexColumnUnknownVersion(t *testing.T) {
	data, err := hex.DecodeString("0364656600000000000c2d00e3930400a00000ff0000035359531144424d535f58415f5849445f4152524159fb")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeColumn(data)
	if err != nil || got.ComplexSchema != "SYS" || got.ComplexName != "DBMS_XA_XID_ARRAY" || got.ComplexVersion != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = DecodeColumn(data[:len(data)-1]); err == nil {
		t.Fatal("truncated version accepted")
	}
}

func TestTextColumn(t *testing.T) {
	data := []byte{3, 'd', 'e', 'f', 0, 0, 0, 1, 'v', 0, 12, 45, 0, 10, 0, 0, 0, 253, 1, 0, 0, 0, 0}
	column, err := DecodeColumn(data)
	if err != nil || column.Catalog != "def" || column.Name != "v" || column.Charset != 45 || column.Length != 10 || column.Type != 253 || column.Flags != 1 {
		t.Fatalf("column decode: %+v %v", column, err)
	}
	if n, err := DecodeColumnCount([]byte{2}, 4096); err != nil || n != 2 {
		t.Fatalf("column count: %d %v", n, err)
	}
	for _, b := range [][]byte{{0}, {252, 0, 32}, {254, 255, 255, 255, 255, 255, 255, 255, 255}, {1, 0}} {
		if _, err := DecodeColumnCount(b, 4096); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid column count %x: %v", b, err)
		}
	}
	for _, n := range []int{0, 1, 10, 17, 21} {
		if _, err := DecodeColumn(data[:n]); !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncated column accepted: %d %v", n, err)
		}
	}
}

func TestTextRowNullEmpty(t *testing.T) {
	row, err := DecodeTextRow([]byte{251, 0, 2, 0, 255}, 3)
	if err != nil || len(row) != 3 || row[0] != nil || row[1] == nil || len(row[1]) != 0 || !bytes.Equal(row[2], []byte{0, 255}) {
		t.Fatalf("NULL/empty/bytes changed: %#v %v", row, err)
	}
	for _, b := range [][]byte{{}, {1}, {251, 0}, {254, 255, 255, 255, 255, 255, 255, 255, 255}} {
		if _, err := DecodeTextRow(b, 1); !errors.Is(err, ErrMalformed) {
			t.Fatalf("bad row accepted %x: %v", b, err)
		}
	}
	ok, err := DecodeEOF([]byte{254, 1, 0, 10, 0})
	if err != nil || ok.Warnings != 1 || ok.Status != 10 {
		t.Fatalf("EOF: %+v %v", ok, err)
	}
}

func FuzzText(f *testing.F) {
	f.Add([]byte{251, 0, 2, 0, 255})
	f.Add([]byte{254, 1, 0, 2, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		_, _ = DecodeColumn(data)
		_, _ = DecodeColumnCount(data, 4096)
		_, _ = DecodeTextRow(data, 3)
		_, _ = DecodeEOF(data)
	})
}
