// Package charset provides strict encoding at OceanBase's text boundaries.
package charset

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

const (
	nameUTF8MB4     = "utf8mb4"
	nameUTF8MB3     = "utf8mb3"
	nameLatin1      = "latin1"
	nameGBK         = "gbk"
	nameGB18030     = "gb18030"
	nameHKSCS       = "hkscs"
	nameHKSCS31     = "hkscs31"
	nameGB180302022 = "gb18030_2022"
	nameASCII       = "ascii"
	nameBinary      = "binary"
)

// ErrEncoding means an encoding is unknown, invalid or cannot represent a value.
var ErrEncoding = errors.New("oceanbase: invalid or unrepresentable character encoding")

// Encode converts Go UTF-8 text to a connection character set without replacement.
func Encode(name, text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, ErrEncoding
	}
	switch strings.ToLower(name) {
	// Connector/J Options.getCharacterEncoding uses UTF-8 payloads for HKSCS names.
	case nameUTF8MB4, nameHKSCS, nameHKSCS31:
		return []byte(text), nil
	case nameGB180302022:
		return encodeGB180302022(text)
	case "utf8", nameUTF8MB3:
		for _, r := range text {
			if r > 65535 {
				return nil, ErrEncoding
			}
		}
		return []byte(text), nil
	case nameLatin1:
		b, err := charmap.Windows1252.NewEncoder().String(text)
		if err != nil {
			return nil, ErrEncoding
		}
		return []byte(b), nil
	case nameBinary:
		return []byte(text), nil
	case nameASCII:
		for _, r := range text {
			if r > 127 {
				return nil, ErrEncoding
			}
		}
		return []byte(text), nil
	default:
		enc := textEncoding(name)
		if enc == nil {
			return nil, ErrEncoding
		}
		data, err := enc.NewEncoder().String(text)
		if err != nil {
			return nil, ErrEncoding
		}
		return []byte(data), nil
	}
}

// Decode converts connection bytes to Go UTF-8 text, rejecting invalid input.
func Decode(name string, data []byte) (string, error) {
	if strings.EqualFold(name, nameGB180302022) {
		return decodeGB180302022(data)
	}
	if strings.EqualFold(name, nameLatin1) {
		s, err := charmap.Windows1252.NewDecoder().Bytes(data)
		if err != nil || bytes.Contains(s, []byte("�")) {
			return "", ErrEncoding
		}
		return string(s), nil
	}
	if enc := textEncoding(name); enc != nil {
		if strings.EqualFold(name, "utf16") && !validUTF16(data) {
			return "", ErrEncoding
		}
		decoded, err := enc.NewDecoder().Bytes(data)
		if err != nil {
			return "", ErrEncoding
		}
		if strings.ContainsRune(string(decoded), utf8.RuneError) && !strings.EqualFold(name, "utf16") && !validReplacement(enc, data) {
			return "", ErrEncoding
		}
		return string(decoded), nil
	}
	text := string(data)
	if _, err := Encode(name, text); err != nil {
		return "", err
	}
	return text, nil
}

func textEncoding(name string) encoding.Encoding {
	switch strings.ToLower(name) {
	case nameGBK:
		return simplifiedchinese.GBK
	case nameGB18030:
		return simplifiedchinese.GB18030
	case "utf16":
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	}
	return nil
}

func validUTF16(data []byte) bool {
	if len(data)%2 != 0 {
		return false
	}
	for i := 0; i < len(data); i += 2 {
		n := binary.BigEndian.Uint16(data[i:])
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || i+4 > len(data) {
			return false
		}
		next := binary.BigEndian.Uint16(data[i+2:])
		if next < 0xdc00 || next > 0xdfff {
			return false
		}
		i += 2
	}
	return true
}

// Check only replacement-bearing input so valid legacy aliases remain readable.
func validReplacement(enc encoding.Encoding, data []byte) bool {
	replacement, _ := enc.NewEncoder().String("�")
	for len(data) > 0 {
		n := 1
		if data[0] > 0x80 {
			n = 2
			if len(data) > 1 && data[1] >= 0x30 && data[1] <= 0x39 {
				n = 4
			}
		}
		if n > len(data) {
			return false
		}
		decoded, err := enc.NewDecoder().Bytes(data[:n])
		if err != nil {
			return false
		}
		if bytes.Contains(decoded, []byte("�")) && !bytes.Equal(data[:n], []byte(replacement)) {
			return false
		}
		data = data[n:]
	}
	return true
}

// Collation identifies supported OceanBase result encodings; unknown IDs are rejected.
func Collation(id uint16) (string, bool) {
	switch {
	case id == 63:
		return nameBinary, true
	case id == 8 || id == 15 || id == 31 || id >= 47 && id <= 49 || id == 94:
		return nameLatin1, true
	case id == 11 || id == 65:
		return nameASCII, true
	case id == 28 || id == 87:
		return nameGBK, true
	case id == 54 || id == 55 || id >= 101 && id <= 124:
		return "utf16", true
	// OceanBase ob_charset.h: CS_TYPE_HKSCS_BIN, CS_TYPE_HKSCS31_BIN,
	// and CS_TYPE_GB18030_2022_BIN through CS_TYPE_GB18030_2022_STROKE_CS.
	case id == 152:
		return nameHKSCS, true
	case id == 153:
		return nameHKSCS31, true
	case id == 33 || id == 76 || id == 83 || id >= 192 && id <= 215:
		return nameUTF8MB3, true
	case id == 45 || id == 46 || id >= 224 && id <= 247 || id == 255:
		return nameUTF8MB4, true
	}
	return gb18030Collation(id)
}

func gb18030Collation(id uint16) (string, bool) {
	switch {
	case id >= 248 && id <= 250:
		return nameGB18030, true
	case id >= 216 && id <= 222:
		return nameGB180302022, true
	}
	return "", false
}
