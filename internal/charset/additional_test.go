package charset

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAdditionalCharsetMappings(t *testing.T) {
	for _, tc := range []struct {
		name, text, encoded string
		general, binary     uint16
	}{
		{nameCP850, "café", "\x63\x61\x66\x82", 4, 80},
		{nameHP8, "ÄÖé", "\xd8\xda\xc5", 6, 72},
		{nameMacRoman, "ÄéΩ", "\x80\x8e\xbd", 39, 53},
		{nameSWE7, "ÅÄÖåäö", "\x5d\x5b\x5c\x7d\x7b\x7c", 10, 82},
		{nameUJIS, "日本語ｱ〜", "\xc6\xfc\xcb\xdc\xb8\xec\x8e\xb1\xa1\xc1", 12, 91},
		{nameCP932, "日本語ｱ～①髙", "\x93\xfa\x96\x7b\x8c\xea\xb1\x81\x60\x87\x40\xfb\xfc", 95, 96},
		{nameEUCJPMS, "日本語ｱ～①髙", "\xc6\xfc\xcb\xdc\xb8\xec\x8e\xb1\xa1\xc1\xad\xa1\x8f\xf4\xfb", 97, 98},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{tc.name, strings.ToUpper(tc.name)} {
				got, err := Encode(name, tc.text)
				if err != nil || !bytes.Equal(got, []byte(tc.encoded)) {
					t.Fatalf("Encode %s: %x %v; want %x", name, got, err, tc.encoded)
				}
				decoded, err := Decode(name, got)
				if err != nil || decoded != tc.text {
					t.Fatalf("Decode %s: %q %v", name, decoded, err)
				}
				if id, ok := HandshakeID(name); !ok || uint16(id) != tc.general {
					t.Fatal("handshake", id, ok)
				}
			}
			for _, id := range []uint16{tc.general, tc.binary} {
				if name, ok := Collation(id); !ok || name != tc.name {
					t.Fatal("collation", id, name, ok)
				}
			}
			for _, text := range []string{"😀", string([]byte{0xff})} {
				if _, err := Encode(tc.name, text); !errors.Is(err, ErrEncoding) {
					t.Fatal("invalid Unicode accepted", err)
				}
			}
		})
	}
}

func TestAdditionalCharsetStrictness(t *testing.T) {
	for _, tc := range []struct{ name, encoded string }{
		{nameCP932, "\x80"}, {nameCP932, "\x81"}, {nameCP932, "\x81\x7f"}, {nameCP932, "\x85\x40"},
		{nameUJIS, "\x80"}, {nameUJIS, "\xa1"}, {nameUJIS, "\x8e"}, {nameUJIS, "\x8e\xa0"}, {nameUJIS, "\x8f\xa1"}, {nameUJIS, "\xad\xa1"},
		{nameEUCJPMS, "\x8f\xa1\x80"}, {nameEUCJPMS, "\x8f\xff\xa1"}, {nameEUCJPMS, "\x8e\xe0"},
		{nameHP8, "\xff"}, {nameSWE7, "\x80"}, {nameSWE7, "\xff"},
	} {
		if _, err := Decode(tc.name, []byte(tc.encoded)); !errors.Is(err, ErrEncoding) {
			t.Errorf("Decode %s %x accepted: %v", tc.name, tc.encoded, err)
		}
	}
	for _, tc := range []struct{ name, text string }{
		{nameSWE7, "["}, {nameSWE7, "\\"}, {nameSWE7, "~"}, {nameCP932, "¥"}, {nameUJIS, "～"},
	} {
		if _, err := Encode(tc.name, tc.text); !errors.Is(err, ErrEncoding) {
			t.Errorf("lossy Encode %s %q accepted: %v", tc.name, tc.text, err)
		}
	}
}

// Reference hashes come from OceanBase v4.4.1_CE mb_wc/wc_mb tables, not x/text.
func TestAdditionalCharsetServerTables(t *testing.T) {
	for _, tc := range []struct{ name, decodeHash, encodeHash string }{
		{nameCP850, "58c4c17496d4e55ba1779e565c4fd4cb390960441cb960b33dbbe593eef68cfc", "325932817402567da6c2422cbb70ffd2c83845e1b294021924542c2fec5e1baf"},
		{nameHP8, "07371ce4c483733efcba365dc430029168bb660eb8f2918e2638892d271b826d", "69a6adb6cda44bb558f5ee81cad9924e0ecced28dc944b6116b90b7f3a8818dc"},
		{nameMacRoman, "6547fd8f75e7eae6583b824ee2233df9405ec928139ce585f2014998e9c92bc6", "d7f62d023db6d4621bda296113df467738d2cad28d1d525eec190d85c815ee9e"},
		{nameSWE7, "b997efe109088213e51cfb4d24503716d1ab892e9aec70176a427551f00f26bb", "5b0397f2d643f8f507f3894e8e309c308931eb3f5382400c7cffbc73e3d6c39a"},
		{nameCP932, "75b24d919bcdf1a0a15eb02957d14fe89083f50488928303420a1f081461e3f5", "23b319b73c93560392ddafdb0a824adccdd81951f88048a438279c03b1560de2"},
		{nameUJIS, "7420514392b3776041505af7e4c4be8b413bdf216b271710948babd81dd34563", "4da4ecd91ddeacf78df4b8c1f732003d233ce6f1a8d37766087a6849d00c0c77"},
		{nameEUCJPMS, "7fd1d8a004d819c151c15e0ce64e2d7e1492b3c92028118b526eadece74e56d4", "84d9c7400ef6fae3581536003a98554950532e5e74a738c657369357e5d8ee6c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sha256.New()
			record := func(code uint32) {
				data := []byte{byte(code)}
				if code > 0xff {
					data = []byte{byte(code >> 8), byte(code)}
				}
				if code > 0xffff {
					data = append([]byte{byte(code >> 16)}, data...)
				}
				decoded, err := Decode(tc.name, data)
				scalar := uint32(0xffffffff)
				if runes := []rune(decoded); err == nil && len(runes) == 1 {
					scalar = uint32(runes[0])
				}
				var entry [8]byte
				binary.BigEndian.PutUint32(entry[:4], code)
				binary.BigEndian.PutUint32(entry[4:], scalar)
				h.Write(entry[:])
			}
			for code := uint32(0); code < 256; code++ {
				record(code)
			}
			if tc.name == nameCP932 || tc.name == nameUJIS || tc.name == nameEUCJPMS {
				for code := uint32(0x8000); code <= 0xffff; code++ {
					record(code)
				}
			}
			if tc.name == nameUJIS || tc.name == nameEUCJPMS {
				for a := uint32(0xa1); a <= 0xfe; a++ {
					for b := uint32(0xa1); b <= 0xfe; b++ {
						record(0x8f0000 | a<<8 | b)
					}
				}
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != tc.decodeHash {
				t.Fatalf("decoder differs from server tables: %s", got)
			}
			h.Reset()
			for r := rune(0); r <= 0xffff; r++ {
				if r >= 0xd800 && r <= 0xdfff {
					continue
				}
				data, err := Encode(tc.name, string(r))
				if err != nil {
					if !errors.Is(err, ErrEncoding) {
						t.Fatal(r, err)
					}
					data = nil
				} else if decoded, err := Decode(tc.name, data); err != nil || decoded != string(r) {
					t.Fatalf("lossy %U via %x: %q %v", r, data, decoded, err)
				}
				var entry [3]byte
				binary.BigEndian.PutUint16(entry[:2], uint16(r))
				entry[2] = byte(len(data))
				h.Write(entry[:])
				h.Write(data)
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != tc.encodeHash {
				t.Fatalf("encoder differs from reversible server mappings: %s", got)
			}
		})
	}
}
