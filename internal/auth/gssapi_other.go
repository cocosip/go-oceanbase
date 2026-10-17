//go:build !windows

package auth

// NewGSSContext leaves Unix credential-cache/GSS providers for their platform adapter.
func NewGSSContext(string, string) (GSSContext, error) {
	return nil, ErrGSSUnavailable
}
