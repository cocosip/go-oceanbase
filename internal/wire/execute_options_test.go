package wire

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestExecuteTypeReuseAndChecksum(t *testing.T) {
	params := []Parameter{{Type: 8, Data: []byte{1, 0, 0, 0, 0, 0, 0, 0}}, {Type: 6}}
	got, err := EncodeStmtExecuteOptions(7, params, 64, ExecuteOptions{ReuseTypes: true, UseChecksum: true, SQLChecksum: 0x12345678})
	want := []byte{23, 7, 0, 0, 0, 0, 0x78, 0x56, 0x34, 0x12, 2, 0, 1, 0, 0, 0, 0, 0, 0, 0}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x: %v", got, want, err)
	}
	combined, err := EncodePrepareExecuteOptions(7, []byte("?"), params, 128, ExecuteOptions{ReuseTypes: true, UseChecksum: true, SQLChecksum: 0x12345678})
	if err != nil || binary.LittleEndian.Uint32(combined[len(combined)-8:]) != 0x12345678 || combined[17] != 0 {
		t.Fatal(combined, err)
	}
	if SQLChecksum([]byte("123456789")) != 0x58e3fa20 {
		t.Fatal("SQL checksum vector")
	}
}

func BenchmarkExecuteTypeHeaders(b *testing.B) {
	params := make([]Parameter, 32)
	for i := range params {
		params[i] = Parameter{Type: 8, Data: []byte{1, 0, 0, 0, 0, 0, 0, 0}}
	}
	for _, reuse := range []bool{false, true} {
		name := "fresh"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := EncodeStmtExecuteOptions(7, params, 4096, ExecuteOptions{ReuseTypes: reuse}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
