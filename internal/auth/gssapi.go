package auth

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// GSS provider errors are independent of the driver's configuration and wire types.
var (
	ErrGSSMechanism   = errors.New("auth: unsupported GSSAPI mechanism")
	ErrGSSUnavailable = errors.New("auth: native GSSAPI provider unavailable")
)

// GSSContext owns one native security context and its credentials.
// Step returns a raw output token and whether the native context is established.
// A nil output means no token; Close is idempotent and releases local resources.
type GSSContext interface {
	Step([]byte) ([]byte, bool, error)
	Close() error
}

// ValidGSSPrincipal bounds a nonempty UTF-8 principal without interpreting it.
func ValidGSSPrincipal(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

// GSSAPISeed parses the server's NUL-terminated SPN and mechanism, allowing one
// additional protocol terminator. An explicit caller SPN overrides the server.
func GSSAPISeed(seed []byte, override string) (spn, mechanism string, err error) {
	if len(seed) > 1050 {
		return "", "", ErrInvalidChallenge
	}
	spnBytes, remaining, found := bytes.Cut(seed, []byte{0})
	if !found || (len(spnBytes) != 0 && !ValidGSSPrincipal(string(spnBytes))) {
		return "", "", ErrInvalidChallenge
	}
	mechanismBytes, tail, found := bytes.Cut(remaining, []byte{0})
	if !found || len(tail) > 1 || (len(tail) == 1 && tail[0] != 0) {
		return "", "", ErrInvalidChallenge
	}
	spn = string(spnBytes)
	if override != "" {
		spn = override
	}
	if !ValidGSSPrincipal(spn) {
		return "", "", ErrInvalidChallenge
	}
	switch strings.ToLower(string(mechanismBytes)) {
	case "", "kerberos":
		mechanism = "Kerberos"
	case "negotiate":
		mechanism = "Negotiate"
	case "ntlm":
		mechanism = "NTLM"
	default:
		return "", "", ErrGSSMechanism
	}
	return spn, mechanism, nil
}
