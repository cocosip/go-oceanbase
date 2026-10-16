// Package charset provides strict encoding at OceanBase's text boundaries.
package charset

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// ErrEncoding means an encoding is unknown, invalid or cannot represent a value.
var ErrEncoding = errors.New("oceanbase: invalid or unrepresentable character encoding")

// Encode converts Go UTF-8 text to a connection character set without replacement.
func Encode(name, text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, ErrEncoding
	}
	switch strings.ToLower(name) {
	case "utf8mb4":
		return []byte(text), nil
	case "utf8", "utf8mb3":
		for _, r := range text {
			if r > 65535 {
				return nil, ErrEncoding
			}
		}
		return []byte(text), nil
	case "latin1":
		b, err := charmap.Windows1252.NewEncoder().String(text)
		if err != nil {
			return nil, ErrEncoding
		}
		return []byte(b), nil
	default:
		return nil, ErrEncoding
	}
}

// Decode converts connection bytes to Go UTF-8 text, rejecting invalid input.
func Decode(name string, data []byte) (string, error) {
	if strings.EqualFold(name, "latin1") {
		s, err := charmap.Windows1252.NewDecoder().Bytes(data)
		if err != nil {
			return "", ErrEncoding
		}
		return string(s), nil
	}
	text := string(data)
	if _, err := Encode(name, text); err != nil {
		return "", err
	}
	return text, nil
}
