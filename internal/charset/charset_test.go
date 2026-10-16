package charset

import (
	"bytes"
	"testing"
)

const testUTF8MB4 = "utf8mb4"
const testLatin1 = "latin1"
const testASCII = "ascii"

const testGBK = "gbk"
const testGB18030 = "gb18030"
const testUTF16 = "utf16"

func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		encoded    []byte
	}{
		{testUTF8MB4, "中文😀", []byte("中文😀")},
		{"utf8", "中文", []byte("中文")},
		{testLatin1, "€é", []byte{128, 233}},
	} {
		encoded, err := Encode(tc.name, tc.text)
		if err != nil || !bytes.Equal(encoded, tc.encoded) {
			t.Fatal(tc.name, encoded, err)
		}
		decoded, err := Decode(tc.name, encoded)
		if err != nil || decoded != tc.text {
			t.Fatal(tc.name, decoded, err)
		}
	}
	for _, tc := range []struct{ name, text string }{{"utf8", "😀"}, {testLatin1, "中文"}, {testUTF8MB4, string([]byte{255})}, {"unknown", "x"}} {
		if _, err := Encode(tc.name, tc.text); err == nil {
			t.Fatal("lossy encoding accepted", tc.name)
		}
	}
	if _, err := Decode(testUTF8MB4, []byte{255}); err == nil {
		t.Fatal("invalid utf8 accepted")
	}
}

func TestCharsetRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		data       []byte
	}{
		{testGBK, "中文", []byte{0xd6, 0xd0, 0xce, 0xc4}},
		{testGB18030, "😀", []byte{0x94, 0x39, 0xfc, 0x36}},
		{testUTF16, "中文😀", []byte{0x4e, 0x2d, 0x65, 0x87, 0xd8, 0x3d, 0xde, 0}},
		{testASCII, "plain", []byte("plain")},
	} {
		data, err := Encode(tc.name, tc.text)
		if err != nil || !bytes.Equal(data, tc.data) {
			t.Fatal(tc.name, data, err)
		}
		text, err := Decode(tc.name, tc.data)
		if err != nil || text != tc.text {
			t.Fatal(tc.name, text, err)
		}
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{testLatin1, []byte{0x90}}, {testGBK, []byte{0x81}}, {testGBK, []byte{0x81, 0x30}}, {testGB18030, []byte{0x81, 0x30, 0x81}},
		{testUTF16, []byte{0xd8, 0}}, {testUTF16, []byte{0xdc, 0}}, {testUTF16, []byte{0}}, {testASCII, []byte{128}},
	} {
		if _, err := Decode(tc.name, tc.data); err == nil {
			t.Fatal("invalid encoding accepted", tc)
		}
	}
	if _, err := Encode(testGBK, "😀"); err == nil {
		t.Fatal("unrepresentable GBK character accepted")
	}
	for _, name := range []string{testUTF16, testGB18030} {
		encoded, err := Encode(name, "�")
		if err != nil {
			t.Fatal(err)
		}
		if decoded, err := Decode(name, encoded); err != nil || decoded != "�" {
			t.Fatal("valid replacement character rejected", name, decoded, err)
		}
	}
}
