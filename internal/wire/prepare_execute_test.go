package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestPrepareExecute(t *testing.T) {
	params := []Parameter{{Type: 6}, {Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 42)}}
	want := []byte{161, 0, 0, 0, 0, 0, 1, 0, 0, 0, 3, 'a', '?', 'b', 2, 0, 0, 0, 1, 1, 6, 0, 8, 0, 42, 0, 0, 0, 0, 0, 0, 0}
	want = append(want, make([]byte, 16)...)
	got, err := EncodePrepareExecute(0, []byte("a?b"), params, int64(len(want)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x: %v", got, want, err)
	}
	if _, err = EncodePrepareExecute(0, []byte("a?b"), params, int64(len(want)-1)); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal(err)
	}
	header := []byte{0, 7, 0, 0, 0, 2, 0, 3, 0, 0, 4, 0, 1, 0, 0, 0, 1}
	h, err := DecodePrepareExecuteOK(header)
	if err != nil || h.ID != 7 || h.Columns != 2 || h.Parameters != 3 || h.Warnings != 4 || h.Flags != 1 || !h.HasRows {
		t.Fatal(h, err)
	}
	for i := range header {
		if _, err = DecodePrepareExecuteOK(header[:i]); err == nil {
			t.Fatal("truncated header accepted", i)
		}
	}
	for _, i := range []int{0, 9, 16} {
		bad := bytes.Clone(header)
		bad[i] = 2
		if _, err = DecodePrepareExecuteOK(bad); err == nil {
			t.Fatal("malformed header accepted", i)
		}
	}
}

func FuzzPrepareExecute(f *testing.F) {
	f.Add([]byte("SELECT ?"), []byte{0, 1, 0})
	f.Fuzz(func(t *testing.T, query, value []byte) {
		if len(query)+len(value) > 4096 {
			t.Skip()
		}
		_, _ = DecodePrepareExecuteOK(value)
		payload, err := EncodePrepareExecute(9, query, []Parameter{{Type: 253, Data: value, LengthEncoded: true}}, 8192)
		if err != nil || len(payload) > 8192 || payload[0] != 161 {
			t.Fatal(len(payload), err)
		}
		if _, err = EncodePrepareExecute(9, query, []Parameter{{Type: 253, Data: value, LengthEncoded: true}}, int64(len(payload)-1)); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatal("limit ignored", err)
		}
	})
}
