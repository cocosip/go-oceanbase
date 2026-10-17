package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestStmtPackets(t *testing.T) {
	header := []byte{0, 7, 0, 0, 0, 2, 0, 1, 0, 0, 3, 0}
	h, err := DecodePrepareOK(header)
	if err != nil || h.ID != 7 || h.Columns != 2 || h.Parameters != 1 || h.Warnings != 3 {
		t.Fatal(h, err)
	}
	for n := 0; n < len(header); n++ {
		if _, err := DecodePrepareOK(header[:n]); err == nil {
			t.Fatal("truncated prepare accepted", n)
		}
	}
	for _, n := range []int{0, 1, 7, 8, 9, 16} {
		params := make([]Parameter, n)
		for i := range params {
			params[i] = Parameter{Type: 6}
		}
		payload, err := EncodeStmtExecute(7, params, 1024)
		if err != nil {
			t.Fatal(err)
		}
		want := []byte{23, 7, 0, 0, 0, 0, 1, 0, 0, 0}
		if n > 0 {
			bits := make([]byte, (n+7)/8)
			for i := 0; i < n; i++ {
				bits[i/8] |= 1 << uint(i%8)
			}
			want = append(want, bits...)
			want = append(want, 1)
			for i := 0; i < n; i++ {
				want = append(want, 6, 0)
			}
		}
		if !bytes.Equal(payload, want) {
			t.Fatalf("n=%d got %x want %x", n, payload, want)
		}
		if _, err = EncodeStmtExecute(7, params, int64(len(want))); err != nil {
			t.Fatal("exact bound", err)
		}
		if _, err = EncodeStmtExecute(7, params, int64(len(want)-1)); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatal("size boundary", err)
		}
	}
	for _, n := range []int{0, 250, 251, 65535, 65536} {
		data := bytes.Repeat([]byte{'x'}, n)
		params := []Parameter{{Type: 253, Data: data, LengthEncoded: true}}
		payload, err := EncodeStmtExecute(1, params, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		r := decoder{data: payload, offset: 14}
		got := r.lenBytes()
		if r.err != nil || r.remaining() != 0 || !bytes.Equal(data, got) {
			t.Fatal(n, r.err)
		}
	}
}

func TestBinaryRowsMalformed(t *testing.T) {
	for _, tc := range []struct {
		data []byte
		cols []Column
	}{
		{nil, []Column{{Type: 1}}},
		{[]byte{0}, []Column{{Type: 1}}},
		{[]byte{0, 0, 1}, []Column{{Type: 8}}},
		{[]byte{0, 0, 254, 255, 255, 255, 255, 255, 255, 255, 255}, []Column{{Type: 253}}},
		{[]byte{0, 0, 4, 1}, []Column{{Type: 12}}},
		{[]byte{0, 0, 0, 1}, []Column{{Type: 253}}},
	} {
		if _, err := DecodeBinaryRow(tc.data, tc.cols); err == nil {
			t.Fatalf("malformed row accepted: %x", tc.data)
		}
	}
	data := binary.LittleEndian.AppendUint64([]byte{0, 0}, math.MaxUint64)
	fields, err := DecodeBinaryRow(data, []Column{{Type: 8}})
	if err != nil || len(fields) != 1 || binary.LittleEndian.Uint64(fields[0]) != math.MaxUint64 {
		t.Fatal(fields, err)
	}
}

func FuzzStmt(f *testing.F) {
	f.Add([]byte{0, 0, 1}, byte(1))
	f.Add([]byte{0, 0, 92, 0, 0, 0, 7, 0, 0, 0, 0, 0, 0, 0}, byte(163))
	f.Add([]byte{0, 0, 4, 't', 'e', 's', 't'}, byte(253))
	f.Fuzz(func(t *testing.T, data []byte, typ byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		_, _ = DecodePrepareOK(data)
		fields, err := DecodeBinaryRow(data, []Column{{Type: typ}, {Type: 8}})
		if err == nil && len(fields) != 2 {
			t.Fatal("wrong field count")
		}
		params := []Parameter{{Type: 253, Data: data, LengthEncoded: true}}
		encoded, err := EncodeStmtExecute(1, params, 8192)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 8192 {
			t.Fatal("size bound")
		}
	})
}
