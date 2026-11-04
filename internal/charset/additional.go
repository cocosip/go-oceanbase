package charset

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
)

var additionalCollations = map[uint16]string{
	4: nameCP850, 80: nameCP850,
	6: nameHP8, 72: nameHP8,
	39: nameMacRoman, 53: nameMacRoman,
	10: nameSWE7, 82: nameSWE7,
	12: nameUJIS, 91: nameUJIS,
	95: nameCP932, 96: nameCP932,
	97: nameEUCJPMS, 98: nameEUCJPMS,
}

func additionalCodec(name string) encoding.Encoding {
	switch name {
	case nameCP850:
		return charmap.CodePage850
	case nameMacRoman:
		return charmap.Macintosh
	case nameCP932:
		return japanese.ShiftJIS
	default:
		return japanese.EUCJP
	}
}

func encodeAdditional(name, text string) ([]byte, error) {
	if table := additionalSingleTables[name]; table != nil {
		out := make([]byte, 0, len(text))
		for _, r := range text {
			if r < 128 && table[r] == r {
				out = append(out, byte(r))
				continue
			}
			found := false
			for code, mapped := range table {
				if mapped == r {
					out = append(out, byte(code))
					found = true
					break
				}
			}
			if !found {
				return nil, ErrEncoding
			}
		}
		return out, nil
	}
	encoder := additionalCodec(name).NewEncoder()
	var out []byte
	start := 0
	for i, r := range text {
		code, ok := additionalEncodeOverrides[name][r]
		if !ok {
			code, ok = encodeJapanesePrivate(name, r)
		}
		if !ok {
			continue
		}
		chunk, err := encoder.String(text[start:i])
		if err != nil {
			return nil, ErrEncoding
		}
		out = append(out, chunk...)
		if code > 0xffff {
			out = append(out, byte(code>>16))
		}
		if code > 0xff {
			out = append(out, byte(code>>8))
		}
		out = append(out, byte(code))
		start = i + utf8.RuneLen(r)
	}
	chunk, err := encoder.String(text[start:])
	if err != nil {
		return nil, ErrEncoding
	}
	out = append(out, chunk...)
	// Server aliases may be readable, but must never silently change input text.
	if decoded, err := decodeAdditional(name, out); err != nil || decoded != text {
		return nil, ErrEncoding
	}
	return out, nil
}

func decodeAdditional(name string, data []byte) (string, error) {
	var out strings.Builder
	if table := additionalSingleTables[name]; table != nil {
		for _, b := range data {
			r := table[b]
			if r < 0 {
				return "", ErrEncoding
			}
			out.WriteRune(r)
		}
		return out.String(), nil
	}
	decoder := additionalCodec(name).NewDecoder()
	flush := func(b []byte) error {
		s, err := decoder.Bytes(b)
		if err != nil || strings.ContainsRune(string(s), utf8.RuneError) {
			return ErrEncoding
		}
		out.Write(s)
		return nil
	}
	start := 0
	for i := 0; i < len(data); {
		n := additionalWidth(name, data[i:])
		if n == 0 {
			return "", ErrEncoding
		}
		var code uint32
		for _, b := range data[i : i+n] {
			code = code<<8 | uint32(b)
		}
		r, ok := additionalDecodeOverrides[name][code]
		if !ok {
			r, ok = decodeJapanesePrivate(name, code)
		}
		if ok {
			if r < 0 {
				return "", ErrEncoding
			}
			if err := flush(data[start:i]); err != nil {
				return "", err
			}
			out.WriteRune(r)
			start = i + n
		}
		i += n
	}
	if err := flush(data[start:]); err != nil {
		return "", err
	}
	return out.String(), nil
}

func additionalWidth(name string, data []byte) int {
	a := data[0]
	if name == nameCP850 || name == nameMacRoman || a < 0x80 {
		return 1
	}
	if name == nameCP932 {
		if a >= 0xa1 && a <= 0xdf {
			return 1
		}
		if len(data) >= 2 && (a >= 0x81 && a <= 0x9f || a >= 0xe0 && a <= 0xfc) && data[1] >= 0x40 && data[1] <= 0xfc && data[1] != 0x7f {
			return 2
		}
		return 0
	}
	if a == 0x8e {
		if len(data) >= 2 && data[1] >= 0xa1 && data[1] <= 0xdf {
			return 2
		}
		return 0
	}
	n := 2
	offset := 0
	if a == 0x8f {
		n = 3
		offset = 1
	}
	if len(data) < n {
		return 0
	}
	for _, b := range data[offset:n] {
		if b < 0xa1 || b > 0xfe {
			return 0
		}
	}
	return n
}

func decodeJapanesePrivate(name string, code uint32) (rune, bool) {
	if name == nameCP932 {
		if code < 0xf040 || code > 0xf9fc {
			return 0, false
		}
		trail := code & 0xff
		if trail > 0x7f {
			trail--
		}
		return rune(0xe000 + (code>>8-0xf0)*188 + trail - 0x40), true
	}
	if name != nameUJIS && name != nameEUCJPMS {
		return 0, false
	}
	prefix := rune(0xe000)
	if code>>16 == 0x8f {
		code &= 0xffff
		prefix += 940
	}
	if code < 0xf5a1 || code > 0xfefe {
		return 0, false
	}
	return prefix + rune((code>>8-0xf5)*94+(code&0xff)-0xa1), true
}

func encodeJapanesePrivate(name string, r rune) (uint32, bool) {
	if r < 0xe000 || r > 0xe757 {
		return 0, false
	}
	i := uint32(r - 0xe000)
	if name == nameCP932 {
		trail := i%188 + 0x40
		if trail >= 0x7f {
			trail++
		}
		return (i/188+0xf0)<<8 | trail, true
	}
	if name != nameUJIS && name != nameEUCJPMS {
		return 0, false
	}
	var prefix uint32
	if i >= 940 {
		prefix = 0x8f0000
		i -= 940
	}
	return prefix | (i/94+0xf5)<<8 | (i%94 + 0xa1), true
}
