package wire

import (
	"bytes"
	"testing"
)

func TestComplexFraming(t *testing.T) {
	shape := &ComplexShape{Fields: []*ComplexShape{nil, {Array: true, Fields: []*ComplexShape{nil}}}}
	column := Column{Type: 160, ComplexShape: shape}
	// Row bitmap, object bitmap, NUMBER, array count/bitmap, RAW, following integer.
	data := []byte{0, 0, 0, 3, '1', '.', '0', 2, 8, 2, 0, 255, 42, 0, 0, 0}
	fields, err := DecodeBinaryRow(data, []Column{column, {Type: 3}})
	if err != nil || len(fields) != 2 || !bytes.Equal(fields[1], []byte{42, 0, 0, 0}) {
		t.Fatal(fields, err)
	}
	v, n, err := DecodeComplex(fields[0], shape, 64)
	if err != nil || n != len(fields[0]) || string(v.Children[0].Data) != "1.0" || !v.Children[1].Children[1].Null {
		t.Fatal(v, n, err)
	}
	if _, _, err = DecodeComplex(fields[0], shape, 2); err == nil {
		t.Fatal("node budget ignored")
	}
}

func TestComplexParameterHeader(t *testing.T) {
	p := Parameter{Type: 160, TypeMetadata: []byte{1, 'S', 1, 'T', 0}, Data: []byte{0, 1, '7'}}
	got, err := EncodeStmtExecute(1, []Parameter{p}, 23)
	want := []byte{23, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 160, 0, 1, 'S', 1, 'T', 0, 0, 1, '7'}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal(got, err)
	}
	if _, err = EncodeStmtExecute(1, []Parameter{p}, int64(len(want)-1)); err != ErrMessageTooLarge {
		t.Fatal("metadata missing from size budget", err)
	}
}
