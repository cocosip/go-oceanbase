package charset

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

var legacyEncodeOverrides = func() map[string]map[rune]uint16 {
	all := make(map[string]map[rune]uint16)
	for name, entries := range legacyDecodeOverrides {
		all[name] = make(map[rune]uint16)
		for code, r := range entries {
			if r >= 128 {
				all[name][r] = code
			}
		}
	}
	for name, entries := range legacyEncodeAdditional {
		if all[name] == nil {
			all[name] = make(map[rune]uint16)
		}
		for r, code := range entries {
			all[name][r] = code
		}
	}
	return all
}()

func legacyCodec(name string) encoding.Encoding {
	switch name {
	case nameSJIS:
		return japanese.ShiftJIS
	case nameBig5:
		return traditionalchinese.Big5
	case nameGB2312:
		return simplifiedchinese.GBK
	case nameEUCKR:
		return korean.EUCKR
	}
	return nil
}

func encodeLegacy(name, text string) ([]byte, error) {
	if high := legacySingleHigh[name]; high != nil {
		out := make([]byte, 0, len(text))
		for _, r := range text {
			if r < 160 {
				out = append(out, byte(r))
				continue
			}
			found := false
			for i, mapped := range high {
				if r == mapped && mapped != 0xffff {
					out = append(out, byte(i+160))
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
	enc := legacyCodec(name)
	var out []byte
	start := 0
	for i, r := range text {
		if code, ok := legacyEncodeOverrides[name][r]; ok {
			chunk, err := enc.NewEncoder().String(text[start:i])
			if err != nil {
				return nil, ErrEncoding
			}
			out = append(out, chunk...)
			out = append(out, byte(code>>8), byte(code))
			start = i + utf8.RuneLen(r)
		}
	}
	chunk, err := enc.NewEncoder().String(text[start:])
	if err != nil {
		return nil, ErrEncoding
	}
	out = append(out, chunk...)
	// Reject Windows aliases/extensions rather than silently changing the value.
	if decoded, err := decodeLegacy(name, out); err != nil || decoded != text {
		return nil, ErrEncoding
	}
	return out, nil
}

func decodeLegacy(name string, data []byte) (string, error) {
	var out strings.Builder
	if high := legacySingleHigh[name]; high != nil {
		for _, b := range data {
			r := rune(b)
			if b >= 160 {
				r = high[int(b)-160]
			}
			if r == 0xffff {
				return "", ErrEncoding
			}
			out.WriteRune(r)
		}
		return out.String(), nil
	}
	enc := legacyCodec(name)
	flush := func(chunk []byte) error {
		decoded, err := enc.NewDecoder().Bytes(chunk)
		if err != nil || strings.ContainsRune(string(decoded), utf8.RuneError) {
			return ErrEncoding
		}
		out.Write(decoded)
		return nil
	}
	start := 0
	for i := 0; i < len(data); {
		a := data[i]
		if a < 128 || name == nameSJIS && a >= 0xa1 && a <= 0xdf {
			i++
			continue
		}
		if i+1 >= len(data) {
			return "", ErrEncoding
		}
		b := data[i+1]
		code := uint16(a)<<8 | uint16(b)
		if !validLegacyPair(name, a, b) {
			return "", ErrEncoding
		}
		if r, ok := legacyDecodeOverrides[name][code]; ok {
			if r < 0 {
				return "", ErrEncoding
			}
			if err := flush(data[start:i]); err != nil {
				return "", err
			}
			out.WriteRune(r)
			start = i + 2
		}
		i += 2
	}
	if err := flush(data[start:]); err != nil {
		return "", err
	}
	return out.String(), nil
}

func validLegacyPair(name string, a, b byte) bool {
	switch name {
	case nameSJIS:
		return validSJISPair(a, b)
	case nameBig5:
		code := uint16(a)<<8 | uint16(b)
		return (code >= 0xa140 && code <= 0xc7fc || code >= 0xc940 && code <= 0xf9dc) && (b >= 0x40 && b <= 0x7e || b >= 0xa1 && b <= 0xfe)
	case nameGB2312:
		return a >= 0xa1 && a <= 0xf7 && b >= 0xa1 && b <= 0xfe
	case nameEUCKR:
		return a >= 0x81 && a <= 0xfe && (b >= 0x41 && b <= 0x5a || b >= 0x61 && b <= 0x7a || b >= 0x81 && b <= 0xfe)
	}
	return false
}

func validSJISPair(a, b byte) bool {
	return (a >= 0x81 && a <= 0x9f && a != 0x87 || a >= 0xe0 && a <= 0xea) && b >= 0x40 && b <= 0xfc && b != 0x7f
}
