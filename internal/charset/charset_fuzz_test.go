package charset

import "testing"

func FuzzCharset(f *testing.F) {
	f.Add(byte(0), []byte("中文😀"))
	f.Add(byte(2), []byte{0x81})
	f.Add(byte(4), []byte{0xd8, 0})
	f.Add(byte(8), []byte{0xfe, 0x59})
	f.Add(byte(8), []byte{0x81, 0x3a, 0x81, 0x30})
	f.Fuzz(func(t *testing.T, kind byte, data []byte) {
		if len(data) > 1024 {
			return
		}
		names := [...]string{testUTF8MB4, "utf8mb3", testGBK, testGB18030, testUTF16, testLatin1, testASCII, "binary", "gb18030_2022", testHKSCS, testHKSCS31}
		name := names[int(kind)%len(names)]
		if text, err := Decode(name, data); err == nil {
			encoded, err := Encode(name, text)
			if err != nil {
				t.Fatal("decoder silently replaced bytes", name, data, text, err)
			}
			if decoded, err := Decode(name, encoded); err != nil || decoded != text {
				t.Fatal("lossy character encoding", name, text, decoded, err)
			}
		}
	})
}
