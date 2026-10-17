//go:build !windows && !(gssapi && ((linux && !android && (amd64 || arm64 || 386)) || (darwin && !ios && (amd64 || arm64))))

package auth

// NewGSSContext rejects targets without an implemented native provider.
func NewGSSContext(string, string) (GSSContext, error) {
	return nil, ErrGSSUnavailable
}
