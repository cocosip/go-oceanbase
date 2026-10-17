package auth

import (
	"bytes"
	"unicode/utf8"
)

// PAMPrompt decodes the dialog question type, optional terminator and last flag.
func PAMPrompt(data []byte) (text string, echo, last bool, err error) {
	if len(data) == 0 || data[0] < 2 || data[0] > 5 {
		return "", false, false, ErrInvalidChallenge
	}
	prompt := bytes.TrimSuffix(data[1:], []byte{0})
	if bytes.IndexByte(prompt, 0) >= 0 || !utf8.Valid(prompt) {
		return "", false, false, ErrInvalidChallenge
	}
	return string(prompt), data[0] < 4, data[0]&1 != 0, nil
}
