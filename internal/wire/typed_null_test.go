package wire

import (
	"bytes"
	"testing"
)

func TestTypedNullParameter(t *testing.T) {
	p := Parameter{Type: 209, Null: true, LengthEncoded: true}
	want := []byte{23, 7, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 209, 0}
	got, err := EncodeStmtExecute(7, []Parameter{p}, int64(len(want)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("typed NULL: %x, %v; want %x", got, err, want)
	}
	got, err = EncodeStmtExecuteOptions(7, []Parameter{p}, 12, ExecuteOptions{ReuseTypes: true})
	if err != nil || !bytes.Equal(got, []byte{23, 7, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0}) {
		t.Fatalf("reused typed NULL: %x %v", got, err)
	}
	rows := [][]Parameter{{p}, {{Type: 209, Data: []byte("AAAA"), LengthEncoded: true}}}
	got, err = EncodeStmtExecuteArray(7, rows, 128, ExecuteOptions{})
	if err != nil || !bytes.Equal(got[18:], []byte{2, 1, 4, 'A', 'A', 'A', 'A'}) {
		t.Fatalf("typed NULL array: %x %v", got, err)
	}
}
