package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestStmtArrayGolden(t *testing.T) {
	rows := [][]Parameter{
		{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 7)}, {Type: 6}},
		{{Type: 6}, {Type: 253, Data: []byte("x"), LengthEncoded: true}},
	}
	want := []byte{23, 9, 0, 0, 0, 24, 1, 0, 0, 0, 0, 1,
		160, 0, 0, 0, 0, 8, 160, 0, 0, 0, 0, 253,
		2, 2, 7, 0, 0, 0, 0, 0, 0, 0, 2, 1, 1, 'x'}
	for _, checksum := range []bool{false, true} {
		expected := bytes.Clone(want)
		options := ExecuteOptions{ReuseTypes: true, UseChecksum: checksum, SQLChecksum: 0x12345678}
		if checksum {
			binary.LittleEndian.PutUint32(expected[6:], options.SQLChecksum)
		}
		got, err := EncodeStmtExecuteArray(9, rows, int64(len(want)), options)
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("array packet: %x, want %x: %v", got, expected, err)
		}
		if _, err = EncodeStmtExecuteArray(9, rows, int64(len(want)-1), options); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatal("array size boundary", err)
		}
	}
}

func TestStmtArrayOracleBinaryDouble(t *testing.T) {
	rows := [][]Parameter{
		{{Type: 257, Data: []byte{255, 255, 255, 255, 255, 255, 239, 127}}},
		{{Type: 257, Data: []byte{255, 255, 255, 255, 255, 255, 239, 255}}},
		{{Type: 6}},
	}
	want := []byte{23, 9, 0, 0, 0, 24, 1, 0, 0, 0, 0, 1,
		160, 0, 0, 0, 0, 5, 3, 4,
		255, 255, 255, 255, 255, 255, 239, 127,
		255, 255, 255, 255, 255, 255, 239, 255}
	got, err := EncodeStmtExecuteArray(9, rows, int64(len(want)), ExecuteOptions{})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("binary double array: %x, want %x: %v", got, want, err)
	}
	if rows[0][0].Type != 257 {
		t.Fatal("array encoding changed the scalar parameter type")
	}
	if _, err = EncodeStmtExecuteArray(9, rows, int64(len(want)-1), ExecuteOptions{}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal("binary double array size boundary", err)
	}
}

func TestStmtArrayNullBitmapAndLengths(t *testing.T) {
	for _, count := range []int{1, 8, 9, 250, 251, 65535} {
		rows := make([][]Parameter, count)
		for i := range rows {
			rows[i] = []Parameter{{Type: 6}, {Type: 253, Data: []byte("ok"), LengthEncoded: true}}
			if i%3 == 0 {
				rows[i][1] = Parameter{Type: 6}
			}
		}
		payload, err := EncodeStmtExecuteArray(3, rows, 1<<20, ExecuteOptions{})
		if err != nil {
			t.Fatal(count, err)
		}
		decoder := decoder{data: payload, offset: 24}
		for column := range 2 {
			if decoder.lenEnc() != uint64(count) {
				t.Fatal("array count", count)
			}
			bitmap := decoder.take((count + 7) / 8)
			for i := range rows {
				null := rows[i][column].Type == 6
				if (bitmap[i/8]&(1<<uint(i%8)) != 0) != null {
					t.Fatal("input bitmap offset", count, column, i)
				}
				if !null && string(decoder.lenBytes()) != "ok" {
					t.Fatal("array scalar body", count, i)
				}
			}
		}
		if decoder.err != nil || decoder.remaining() != 0 {
			t.Fatal("array boundary", count, decoder.err)
		}
	}
	for _, n := range []int{0, 250, 251, 65535, 65536} {
		value := bytes.Repeat([]byte{'x'}, n)
		payload, err := EncodeStmtExecuteArray(1, [][]Parameter{{{Type: 253, Data: value, LengthEncoded: true}}}, 1<<20, ExecuteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d := decoder{data: payload, offset: 20}
		if !bytes.Equal(d.lenBytes(), value) || d.err != nil || d.remaining() != 0 {
			t.Fatal("length encoding", n, d.err)
		}
	}
}

func TestStmtArrayRejectsInvalidInputs(t *testing.T) {
	for _, rows := range [][][]Parameter{
		nil, {{}}, {{{Type: 6}}, {}},
		{{{Type: 8}}, {{Type: 253}}},
		{{{Type: 8 | 128<<8}}},
		{{{Type: 160, TypeMetadata: []byte{0}}}},
		{{{Type: 253, LongData: true}}},
		{make([]Parameter, 65536)}, make([][]Parameter, 65536),
	} {
		if _, err := EncodeStmtExecuteArray(1, rows, 1<<20, ExecuteOptions{}); !errors.Is(err, ErrMalformed) {
			t.Fatal("invalid array accepted", err)
		}
	}
	for _, dataBytes := range []int64{-1, math.MaxInt64} {
		if _, err := StmtArraySize(9, 2, dataBytes); err == nil {
			t.Fatal("invalid size accepted", dataBytes)
		}
	}
}

func FuzzStmtArray(f *testing.F) {
	f.Add([]byte("example"), byte(9))
	f.Add([]byte{}, byte(8))
	f.Fuzz(func(t *testing.T, value []byte, n byte) {
		if len(value) > 4096 {
			t.Skip()
		}
		count := int(n)%32 + 1
		rows := make([][]Parameter, count)
		var bodyBytes int64
		for i := range rows {
			parameter := Parameter{Type: 253, Data: value, LengthEncoded: true}
			if i%3 == 0 {
				parameter = Parameter{Type: 6}
			}
			rows[i] = []Parameter{parameter}
			bodyBytes += parameter.EncodedSize()
		}
		size, err := StmtArraySize(count, 1, bodyBytes)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := EncodeStmtExecuteArray(1, rows, size, ExecuteOptions{})
		if err != nil || int64(len(payload)) != size {
			t.Fatal("array size", size, len(payload), err)
		}
		d := decoder{data: payload, offset: 18}
		if d.lenEnc() != uint64(count) {
			t.Fatal("array row count")
		}
		bitmap := d.take((count + 7) / 8)
		for i := range rows {
			if (bitmap[i/8]&(1<<uint(i%8)) != 0) != (i%3 == 0) {
				t.Fatal("array bitmap")
			}
			if i%3 != 0 && !bytes.Equal(d.lenBytes(), value) {
				t.Fatal("array scalar value")
			}
		}
		if d.err != nil || d.remaining() != 0 {
			t.Fatal("array response boundary", d.err)
		}
	})
}
