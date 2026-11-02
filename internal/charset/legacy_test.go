package charset

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

const (
	testLegacySJIS    = "sjis"
	testLegacyBig5    = "big5"
	testLegacyGB2312  = "gb2312"
	testLegacyEUCKR   = "euckr"
	testLegacyDEC8    = "dec8"
	testLegacyTIS620  = "tis620"
	testLegacyUTF16LE = "utf16le"
)

func TestLegacyCharsetMappings(t *testing.T) {
	for _, tc := range []struct {
		name, text, encoded string
		collations          []uint16
		handshake           byte
	}{
		{testLegacySJIS, "日本語ｱ〜", "\x93\xfa\x96\x7b\x8c\xea\xb1\x81\x60", []uint16{13, 88, 334, 349, 364}, 13},
		{testLegacyBig5, "中文", "\xa4\xa4\xa4\xe5", []uint16{1, 84, 335, 350, 365}, 1},
		{testLegacyGB2312, "中文", "\xd6\xd0\xce\xc4", []uint16{24, 86}, 24},
		{testLegacyEUCKR, "한글", "\xc7\xd1\xb1\xdb", []uint16{19, 85}, 19},
		{testLegacyDEC8, "ŒŸœÿ", "\xd7\xdd\xf7\xfd", []uint16{3, 69, 338, 353, 368}, 3},
		{testLegacyTIS620, "กข฿", "\xa1\xa2\xdf", []uint16{18, 89, 332, 347, 362}, 18},
		{testLegacyUTF16LE, "中😀", "\x2d\x4e\x3d\xd8\x00\xde", []uint16{56, 62, 333, 348, 363}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.name, tc.text)
			if err != nil || !bytes.Equal(got, []byte(tc.encoded)) {
				t.Fatalf("Encode: %x, %v; want %x", got, err, tc.encoded)
			}
			gotText, err := Decode(tc.name, []byte(tc.encoded))
			if err != nil || gotText != tc.text {
				t.Fatalf("Decode: %q, %v; want %q", gotText, err, tc.text)
			}
			id, ok := HandshakeID(tc.name)
			if id != tc.handshake || ok != (tc.handshake != 0) {
				t.Fatalf("HandshakeID: %d, %v", id, ok)
			}
			for _, id := range tc.collations {
				if name, ok := Collation(id); !ok || name != tc.name {
					t.Fatalf("collation %d: %q, %v", id, name, ok)
				}
			}
		})
	}
}

func TestLegacyCharsetServerTables(t *testing.T) {
	// Independent hashes of OceanBase mb_wc tables, with undefined/FFFD slots
	// rejected. Hash uint16 code + uint32 scalar (ffffffff for invalid), first
	// all single bytes, then 8000..ffff pairs. Tables: v4.3.5_CE SJIS/Big5/DEC8/
	// TIS620 and v4.4.1_CE GB2312/EUCKR, deps/oblib/src/lib/charset/ob_ctype_*.
	for _, tc := range []struct{ name, want string }{
		{testLegacySJIS, "97e46e4d31850e3195e1be22e4520a7a5408cf9c16b8de504890d2ba6388e94b"},
		{testLegacyBig5, "f9fad840685f3c8afa3b0c2ca95fec56aa49df4a960f57fc7bb0db60a58d8d0b"},
		{testLegacyGB2312, "8a1ce4b5b0a0df0ce8bab2588492e3a9e05b6bb7eeac5cc5ff02a157650ff7ea"},
		{testLegacyEUCKR, "00288582f16d955a909c1153e23d0dd13d45276c845a8adc98d1b2ec28142b78"},
		{testLegacyDEC8, "7d32b5ba7dc09e2a416ff6a6a39903d9ee8d7e2d3c85bf7114e008fbf5a6fa1f"},
		{testLegacyTIS620, "ff0fddf52c9f9c0731ef656651bcc7400cf04fb3df58616e252b25a24f885781"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sha256.New()
			for code := 0; code <= 0xffff; code++ {
				if code == 256 {
					if tc.name == testLegacyDEC8 || tc.name == testLegacyTIS620 {
						break
					}
					code = 0x8000
				}
				data := []byte{byte(code >> 8), byte(code)}
				if code < 256 {
					data = data[1:]
				}
				value, err := Decode(tc.name, data)
				scalar := uint32(0xffffffff)
				if runes := []rune(value); err == nil && len(runes) == 1 {
					scalar = uint32(runes[0])
					encoded, encodeErr := Encode(tc.name, value)
					if encodeErr != nil {
						t.Fatalf("cannot re-encode %U from %x: %v", runes[0], data, encodeErr)
					}
					if decoded, err := Decode(tc.name, encoded); err != nil || decoded != value {
						t.Fatalf("lossy re-encoding %x -> %x: %q, %v", data, encoded, decoded, err)
					}
				}
				var record [6]byte
				binary.BigEndian.PutUint16(record[:2], uint16(code))
				binary.BigEndian.PutUint32(record[2:], scalar)
				h.Write(record[:])
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != tc.want {
				t.Fatalf("decoder differs from independent server table: %s", got)
			}
		})
	}
}

func TestLegacyCharsetRejectsInvalid(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{testLegacySJIS, "\x81"}, {testLegacySJIS, "\x87\x40"}, {testLegacySJIS, "\xfa\x40"}, {testLegacySJIS, "\x80"},
		{testLegacyBig5, "\xa4"}, {testLegacyBig5, "\xa1\x5a"}, {testLegacyBig5, "\x81\x40"},
		{testLegacyGB2312, "\x81\x40"}, {testLegacyGB2312, "\xa1\x80"}, {testLegacyGB2312, "\xa1"},
		{testLegacyEUCKR, "\x81"}, {testLegacyEUCKR, "\x81\x40"}, {testLegacyEUCKR, "\xff"},
		{testLegacyDEC8, "\xa4"}, {testLegacyDEC8, "\xff"}, {testLegacyTIS620, "\xa0"}, {testLegacyTIS620, "\xdb"},
		{testLegacyUTF16LE, "\x00"}, {testLegacyUTF16LE, "\x00\xd8"}, {testLegacyUTF16LE, "\x00\xdc"},
	} {
		if _, err := Decode(tc.name, []byte(tc.data)); !errors.Is(err, ErrEncoding) {
			t.Errorf("Decode(%s, %x) = %v; want ErrEncoding", tc.name, tc.data, err)
		}
	}
	for _, name := range []string{testLegacySJIS, testLegacyBig5, testLegacyGB2312, testLegacyEUCKR, testLegacyDEC8, testLegacyTIS620} {
		if _, err := Encode(name, "😀"); !errors.Is(err, ErrEncoding) {
			t.Errorf("Encode(%s, supplementary) = %v", name, err)
		}
	}
	for _, tc := range []struct{ name, text string }{
		{testLegacySJIS, "～"}, {testLegacySJIS, "＼"}, {testLegacySJIS, "①"}, {testLegacySJIS, "髙"},
		{testLegacyBig5, "‧"}, {testLegacyBig5, "髙"}, {testLegacyGB2312, "丂"}, {testLegacyDEC8, "Ð"}, {testLegacyTIS620, "\u00a0"},
	} {
		if _, err := Encode(tc.name, tc.text); !errors.Is(err, ErrEncoding) {
			t.Errorf("lossy/unassigned Encode(%s, %q) = %v", tc.name, tc.text, err)
		}
	}
}
