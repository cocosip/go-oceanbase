package wire

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestOceanBase44LOBLocator(t *testing.T) {
	// Captured from OceanBase 4.4.2.2; Connector/J 2.4.14 reads the payload as 0102.
	data, err := hex.DecodeString("0184010002210100000000000000000002a2070000000000110000000e00100087000000020000007100970d030000000000e9030000000000003f000000000000000100000000000000040000002d0210ac000000000000000000000000420b0000830700eb2d5e0600d002000001928080800001818080800000d0c893cfbaebf8ef1800000200000000000003e90100ffffffffffffffffff0167414144445a63414141414141513d3d018400000102")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLOBLocator(data)
	if err != nil || !got.InRow || got.ByteLength != 2 || string(got.Data) != "\x01\x02" {
		t.Fatalf("%+v: %v", got, err)
	}
	data[0] = 0xff
	if _, err = DecodeLOBLocator(data); err == nil {
		t.Fatal("invalid header accepted")
	}
}

func TestLOBLocator(t *testing.T) {
	v1 := make([]byte, 45)
	copy(v1, []byte{'!', 'B', 'O', 'L', 1})
	binary.LittleEndian.PutUint32(v1[32:], 2)
	binary.LittleEndian.PutUint32(v1[36:], 3)
	copy(v1[40:], "IDabc")
	v2simple := []byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0, 'x', 'y'}
	v2extern := make([]byte, 43)
	copy(v2extern, []byte{'!', 'B', 'O', 'L', 2, 0x20, 1})
	binary.LittleEndian.PutUint32(v2extern[32:], 1)
	binary.LittleEndian.PutUint32(v2extern[36:], 2)
	copy(v2extern[40:], "Iab")
	v2disk := make([]byte, 38)
	copy(v2disk, []byte{'!', 'B', 'O', 'L', 2, 0x20})
	v2disk[9] = 5
	binary.LittleEndian.PutUint64(v2disk[28:], 2)
	copy(v2disk[36:], "ab")
	v2out := make([]byte, 36)
	copy(v2out, []byte{'!', 'B', 'O', 'L', 2})
	v2out[9] = 1
	binary.LittleEndian.PutUint64(v2out[28:], 1<<40)
	v2outExtern := make([]byte, 75)
	copy(v2outExtern, []byte{'!', 'B', 'O', 'L', 2, 0x11, 1})
	binary.LittleEndian.PutUint16(v2outExtern[30:], 2)
	binary.LittleEndian.PutUint16(v2outExtern[40:], 3)
	v2outExtern[48] = 1
	binary.LittleEndian.PutUint64(v2outExtern[67:], 99)
	for _, tc := range []struct {
		name, data string
		packet     []byte
		version    byte
		kind       byte
		length     uint64
		inRow      bool
	}{
		{"v1", "abc", v1, 1, 0, 3, true},
		{"v2-simple", "xy", v2simple, 2, 0, 2, true},
		{"v2-extern", "ab", v2extern, 2, 0, 2, true},
		{"v2-disk", "ab", v2disk, 2, 0, 2, true},
		{"v2-out", "", v2out, 2, 0, 1 << 40, false},
		{"v2-out-extern-temporary", "", v2outExtern, 2, 1, 99, false},
		{"empty", "", []byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, 2, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeLOBLocator(tc.packet)
			if err != nil || got.Version != tc.version || got.Kind != tc.kind || got.ByteLength != tc.length || got.InRow != tc.inRow || string(got.Data) != tc.data {
				t.Fatalf("%+v %q: %v", got, got.Data, err)
			}
		})
	}
	for _, packet := range [][]byte{nil, {}, v1[:44], v2disk[:37], v2extern[:42], v2out[:35], v2outExtern[:74], {'!', 'B', 'O', 'L', 3, 0, 0, 0}, {'!', 'B', 'O', 'L', 2, 0x80, 0, 0}} {
		if _, err := DecodeLOBLocator(packet); err == nil {
			t.Fatalf("invalid locator accepted: %x", packet)
		}
	}
}

func FuzzLOBLocator(f *testing.F) {
	f.Add([]byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0, 'a'})
	f.Add([]byte{'!', 'B', 'O', 'L', 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := DecodeLOBLocator(data)
		if err == nil && got.InRow && (got.ByteLength != uint64(len(got.Data)) || len(got.Data) > len(data)) {
			t.Fatalf("locator length escaped input: %+v", got)
		}
	})
}
