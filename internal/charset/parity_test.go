package charset

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
)

const testHKSCS = "hkscs"
const testHKSCS31 = "hkscs31"

func TestGB180302022IndependentVectors(t *testing.T) {
	// GB18030-2022 remaps from OceanBase ob_ctype_gb18030.cc:init_gb18030_2022;
	// the 2005 U+1E3F remap and PUA vectors also match OpenJDK 21.0.12.1.
	for _, tc := range []struct{ text, data string }{
		{"ḿ", "\xa8\xbc"}, {"\ue7c7", "\x81\x35\xf4\x37"},
		{"龴龵龶龷龸龹龺龻", "\xfe\x59\xfe\x61\xfe\x66\xfe\x67\xfe\x6d\xfe\x7e\xfe\x90\xfe\xa0"},
		{"\ue81e\ue826\ue82b\ue82c\ue832\ue843\ue854\ue864", "\x82\x35\x90\x37\x82\x35\x90\x38\x82\x35\x90\x39\x82\x35\x91\x30\x82\x35\x91\x31\x82\x35\x91\x32\x82\x35\x91\x33\x82\x35\x91\x34"},
		{"\ufe10\ufe11\ufe12\ufe13\ufe14\ufe15\ufe16\ufe17\ufe18\ufe19", "\xa6\xd9\xa6\xdb\xa6\xda\xa6\xdc\xa6\xdd\xa6\xde\xa6\xdf\xa6\xec\xa6\xed\xa6\xf3"},
		{"\ue78d\ue78f\ue78e\ue790\ue791\ue792\ue793\ue794\ue795\ue796", "\x84\x31\x82\x36\x84\x31\x82\x37\x84\x31\x82\x38\x84\x31\x82\x39\x84\x31\x83\x30\x84\x31\x83\x31\x84\x31\x83\x32\x84\x31\x83\x33\x84\x31\x83\x34\x84\x31\x83\x35"},
		{"\ue000\ue05d\ue05e\ue5e5", "\xaa\xa1\xaa\xfe\xab\xa1\xa3\xa0"},
		{"中文😀\U0010ffff�", "\xd6\xd0\xce\xc4\x94\x39\xfc\x36\xe3\x32\x9a\x35\x84\x31\xa4\x37"},
	} {
		b, err := Encode("gb18030_2022", tc.text)
		if err != nil || !bytes.Equal(b, []byte(tc.data)) {
			t.Fatalf("encode %U: %x %v, want %x", []rune(tc.text), b, err, tc.data)
		}
		s, err := Decode("gb18030_2022", []byte(tc.data))
		if err != nil || s != tc.text {
			t.Fatalf("decode %x: %U %v, want %U", tc.data, []rune(s), err, []rune(tc.text))
		}
	}
	if b, err := Encode("gb18030", "龴"); err != nil || !bytes.Equal(b, []byte{0x82, 0x35, 0x90, 0x37}) {
		t.Fatalf("legacy mapping changed: %x %v", b, err)
	}
	for _, b := range [][]byte{{0x80}, {0xff}, {0x81}, {0x81, 0x30}, {0x81, 0x30, 0x81}, {0x81, 0x3a, 0x81, 0x30}, {0x81, 0x30, 0x80, 0x30}, {0x81, 0x30, 0x81, 0x3a}, {0x84, 0x31, 0xa5, 0x30}, {0xe3, 0x32, 0x9a, 0x36}} {
		if _, err := Decode("gb18030_2022", b); err == nil {
			t.Fatalf("invalid sequence accepted: %x", b)
		}
	}
	if _, err := Encode("gb18030_2022", string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}

func TestGB180302022CompleteBMP(t *testing.T) {
	// Independent reference: OpenJDK 21.0.12.1+1 GB18030 (default 2022).
	// SHA256 over [uint16 big-endian codepoint, byte length, encoded bytes] for
	// every BMP scalar in ascending order; this covers all 2,087 x/text differences.
	h := sha256.New()
	for r := rune(0); r <= 0xffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		b, err := Encode("gb18030_2022", string(r))
		if err != nil {
			t.Fatalf("%U: %v", r, err)
		}
		var prefix [3]byte
		binary.BigEndian.PutUint16(prefix[:2], uint16(r))
		prefix[2] = byte(len(b))
		h.Write(prefix[:])
		h.Write(b)
		s, err := Decode("gb18030_2022", b)
		if err != nil || s != string(r) {
			t.Fatalf("%U round trip via %x: %U %v", r, b, []rune(s), err)
		}
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "bce50fc198c82048feb3391c9741666a12aef1fffaeafdf6ced6b26da090e2d7" {
		t.Fatalf("mapping differs from independent JDK reference: %s", got)
	}
}

func TestJavaCharsetDeclarations(t *testing.T) {
	for _, name := range []string{testHKSCS, testHKSCS31} {
		s := "嘅_係_嘢_冇_咁_镬_嘥_😀"
		b, e := Encode(name, s)
		if e != nil || string(b) != s {
			t.Fatal(name, b, e)
		}
		out, e := Decode(name, b)
		if e != nil || out != s {
			t.Fatal(name, out, e)
		}
		if _, e = Decode(name, []byte{0xff}); e == nil {
			t.Fatal("invalid UTF8 accepted", name)
		}
	}
	for id := uint16(216); id <= 222; id++ {
		if s, ok := Collation(id); !ok || s != "gb18030_2022" {
			t.Fatal(id, s, ok)
		}
	}
	for id, name := range map[uint16]string{152: testHKSCS, 153: testHKSCS31} {
		if s, ok := Collation(id); !ok || s != name {
			t.Fatal(id, s, ok)
		}
	}
}

func TestGB180302022CompleteDecoder(t *testing.T) {
	// Independent OpenJDK 21.0.12.1 reference: all 23,940 legal two-byte positions
	// followed by all 39,420 BMP four-byte positions. Hash each byte sequence and
	// uint32 big-endian decoded scalar (0xffffffff for an invalid sequence).
	h := sha256.New()
	for n := 0; n < 23940+39420; n++ {
		var b []byte
		if n < 23940 {
			trail := n%190 + 0x40
			if trail >= 0x7f {
				trail++
			}
			b = []byte{byte(n/190 + 0x81), byte(trail)}
		} else {
			x := n - 23940
			b = make([]byte, 4)
			b[3] = byte(x%10 + 0x30)
			x /= 10
			b[2] = byte(x%126 + 0x81)
			x /= 126
			b[1] = byte(x%10 + 0x30)
			b[0] = byte(x/10 + 0x81)
		}
		s, err := Decode("gb18030_2022", b)
		r := uint32(0xffffffff)
		if err == nil {
			r = uint32([]rune(s)[0])
		}
		var code [4]byte
		binary.BigEndian.PutUint32(code[:], r)
		h.Write(b)
		h.Write(code[:])
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "2059e447a987af0772a082f80d0e8fb1a3c046858a42e906e4aa951d4acc3e7d" {
		t.Fatalf("decoder differs from independent JDK reference: %s", got)
	}
}
