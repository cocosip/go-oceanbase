package charset

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// GB18030-2022 differs from x/text's WHATWG mapping, including standard PUA
// assignments. These ranges match OpenJDK 21.0.12.1 GB18030 (2022 default):
// https://github.com/openjdk/jdk/blob/jdk-21%2B35/src/java.base/share/classes/sun/nio/cs/GB18030.java
// The 18 new swaps are also specified by OceanBase init_gb18030_2022:
// https://github.com/oceanbase/oceanbase/blob/e78e4568b663baeef5497ce1a69868c59292bf69/deps/oblib/src/lib/charset/ob_ctype_gb18030.cc
var gb180302022Ranges = [...]struct {
	first, last rune
	code        uint32
}{
	{0x1e3f, 0x1e3f, 0xa8bc},
	{0x9fb4, 0x9fb4, 0xfe59},
	{0x9fb5, 0x9fb5, 0xfe61},
	{0x9fb6, 0x9fb7, 0xfe66},
	{0x9fb8, 0x9fb8, 0xfe6d},
	{0x9fb9, 0x9fb9, 0xfe7e},
	{0x9fba, 0x9fba, 0xfe90},
	{0x9fbb, 0x9fbb, 0xfea0},
	{0xe000, 0xe05d, 0xaaa1},
	{0xe05e, 0xe0bb, 0xaba1},
	{0xe0bc, 0xe119, 0xaca1},
	{0xe11a, 0xe177, 0xada1},
	{0xe178, 0xe1d5, 0xaea1},
	{0xe1d6, 0xe233, 0xafa1},
	{0xe234, 0xe291, 0xf8a1},
	{0xe292, 0xe2ef, 0xf9a1},
	{0xe2f0, 0xe34d, 0xfaa1},
	{0xe34e, 0xe3ab, 0xfba1},
	{0xe3ac, 0xe409, 0xfca1},
	{0xe40a, 0xe467, 0xfda1},
	{0xe468, 0xe4c5, 0xfea1},
	{0xe4c6, 0xe504, 0xa140},
	{0xe505, 0xe525, 0xa180},
	{0xe526, 0xe564, 0xa240},
	{0xe565, 0xe585, 0xa280},
	{0xe586, 0xe5c4, 0xa340},
	{0xe5c5, 0xe5e5, 0xa380},
	{0xe5e6, 0xe624, 0xa440},
	{0xe625, 0xe645, 0xa480},
	{0xe646, 0xe684, 0xa540},
	{0xe685, 0xe6a5, 0xa580},
	{0xe6a6, 0xe6e4, 0xa640},
	{0xe6e5, 0xe705, 0xa680},
	{0xe706, 0xe744, 0xa740},
	{0xe745, 0xe765, 0xa780},
	{0xe766, 0xe76b, 0xa2ab},
	{0xe76d, 0xe76d, 0xa2e4},
	{0xe76e, 0xe76f, 0xa2ef},
	{0xe770, 0xe771, 0xa2fd},
	{0xe772, 0xe77c, 0xa4f4},
	{0xe77d, 0xe784, 0xa5f7},
	{0xe785, 0xe78c, 0xa6b9},
	{0xe78d, 0xe78d, 0x84318236},
	{0xe78e, 0xe78e, 0x84318238},
	{0xe78f, 0xe78f, 0x84318237},
	{0xe790, 0xe790, 0x84318239},
	{0xe791, 0xe796, 0x84318330},
	{0xe797, 0xe79f, 0xa6f6},
	{0xe7a0, 0xe7ae, 0xa7c2},
	{0xe7af, 0xe7bb, 0xa7f2},
	{0xe7bc, 0xe7c6, 0xa896},
	{0xe7c7, 0xe7c7, 0x8135f437},
	{0xe7c9, 0xe7cc, 0xa8c1},
	{0xe7cd, 0xe7e1, 0xa8ea},
	{0xe7e2, 0xe7e2, 0xa958},
	{0xe7e3, 0xe7e3, 0xa95b},
	{0xe7e4, 0xe7e6, 0xa95d},
	{0xe7f4, 0xe800, 0xa997},
	{0xe801, 0xe80f, 0xa9f0},
	{0xe810, 0xe814, 0xd7fa},
	{0xe816, 0xe818, 0xfe51},
	{0xe81e, 0xe81e, 0x82359037},
	{0xe826, 0xe826, 0x82359038},
	{0xe82b, 0xe82b, 0x82359039},
	{0xe82c, 0xe82c, 0x82359130},
	{0xe831, 0xe831, 0xfe6c},
	{0xe832, 0xe832, 0x82359131},
	{0xe83b, 0xe83b, 0xfe76},
	{0xe843, 0xe843, 0x82359132},
	{0xe854, 0xe854, 0x82359133},
	{0xe855, 0xe855, 0xfe91},
	{0xe864, 0xe864, 0x82359134},
	{0xfe10, 0xfe10, 0xa6d9},
	{0xfe11, 0xfe11, 0xa6db},
	{0xfe12, 0xfe12, 0xa6da},
	{0xfe13, 0xfe16, 0xa6dc},
	{0xfe17, 0xfe18, 0xa6ec},
	{0xfe19, 0xfe19, 0xa6f3},
}

var gb180302022Encode, gb180302022Decode = gb180302022Overrides()

func gb180302022Overrides() (map[rune]uint32, map[uint32]rune) {
	enc, dec := make(map[rune]uint32), make(map[uint32]rune)
	for _, block := range gb180302022Ranges {
		for r := block.first; r <= block.last; r++ {
			code := block.code + uint32(r-block.first)
			enc[r] = code
			dec[code] = r
		}
	}
	return enc, dec
}

func encodeGB180302022(text string) ([]byte, error) {
	data := make([]byte, 0, len(text))
	encoder := simplifiedchinese.GB18030.NewEncoder()
	var source, encoded [utf8.UTFMax]byte
	for _, r := range text {
		if r < utf8.RuneSelf {
			data = append(data, byte(r))
		} else if code, ok := gb180302022Encode[r]; ok {
			if code > 0xffff {
				data = append(data, byte(code>>24), byte(code>>16))
			}
			data = append(data, byte(code>>8), byte(code))
		} else {
			size := utf8.EncodeRune(source[:], r)
			n, consumed, err := encoder.Transform(encoded[:], source[:size], true)
			if err != nil || consumed != size {
				return nil, ErrEncoding
			}
			data = append(data, encoded[:n]...)
		}
	}
	return data, nil
}

func decodeGB180302022(data []byte) (string, error) {
	var text strings.Builder
	decoder := simplifiedchinese.GB18030.NewDecoder()
	var decoded [utf8.UTFMax]byte
	for len(data) > 0 {
		if data[0] < utf8.RuneSelf {
			text.WriteByte(data[0])
			data = data[1:]
			continue
		}
		n := 2
		if data[0] < 0x81 || data[0] > 0xfe || len(data) < 2 {
			return "", ErrEncoding
		}
		if data[1] >= 0x30 && data[1] <= 0x39 {
			if len(data) < 4 || data[2] < 0x81 || data[2] > 0xfe || data[3] < 0x30 || data[3] > 0x39 {
				return "", ErrEncoding
			}
			n = 4
		} else if data[1] < 0x40 || data[1] == 0x7f || data[1] > 0xfe {
			return "", ErrEncoding
		}
		var code uint32
		for _, b := range data[:n] {
			code = code<<8 | uint32(b)
		}
		if r, ok := gb180302022Decode[code]; ok {
			text.WriteRune(r)
		} else {
			size, consumed, err := decoder.Transform(decoded[:], data[:n], true)
			if err != nil || consumed != n {
				return "", ErrEncoding
			}
			// The valid encoded replacement character is distinct from decoder substitution.
			r, _ := utf8.DecodeRune(decoded[:size])
			if r == utf8.RuneError && code != 0x8431a437 {
				return "", ErrEncoding
			}
			text.Write(decoded[:size])
		}
		data = data[n:]
	}
	return text.String(), nil
}
