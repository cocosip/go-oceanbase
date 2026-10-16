package charset

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		encoded    []byte
	}{
		{"utf8mb4", "中文😀", []byte("中文😀")},
		{"utf8", "中文", []byte("中文")},
		{"latin1", "€é", []byte{128, 233}},
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
	for _, tc := range []struct{ name, text string }{{"utf8", "😀"}, {"latin1", "中文"}, {"utf8mb4", string([]byte{255})}, {"unknown", "x"}} {
		if _, err := Encode(tc.name, tc.text); err == nil {
			t.Fatal("lossy encoding accepted", tc.name)
		}
	}
	if _, err := Decode("utf8mb4", []byte{255}); err == nil {
		t.Fatal("invalid utf8 accepted")
	}
}
