package auth

import (
	"errors"
	"strings"
)

// Clear encodes a password terminated by NUL. The driver must enforce TLS first.
func Clear(password string) ([]byte, error) {
	if strings.ContainsRune(password, 0) {
		return nil, errors.New("auth: password contains a NUL byte")
	}
	return append([]byte(password), 0), nil
}
