package config

import (
	"context"
	"crypto/rsa"
)

// Credentials are obtained for each new authentication, never during a transaction.
// ProxyUser selects an Oracle proxy-login target; empty authenticates as User alone.
type Credentials struct{ User, Password, ProxyUser string }

// AuthSecurity describes the transport needed before a plugin sees credentials.
type AuthSecurity uint8

// Authentication security requirements. Unknown values are rejected.
const (
	AuthChallengeResponse AuthSecurity = iota
	AuthRequireTLS
)

// AuthChallenge contains copies of server data and driver-verified TLS state.
type AuthChallenge struct {
	Nonce, Data       []byte
	More, TLSVerified bool
}

// AuthPlugin produces authentication messages without access to the connection.
// Security must declare any stage that exposes credentials as AuthRequireTLS.
// If it implements io.Closer, the driver closes it on switch or completion,
// including failure. Close must release local state promptly without network I/O.
type AuthPlugin interface {
	Security(AuthChallenge) AuthSecurity
	Respond(context.Context, Credentials, AuthChallenge) ([]byte, error)
}

// AuthConversation optionally exchanges raw tokens until Complete reports true.
// The driver preserves complete server packets, including their first byte,
// and still applies Security before every Respond. A nil continuation response
// sends no packet; a non-nil empty response sends an empty packet. Completion
// still requires a final server OK. Each use must have independent state.
type AuthConversation interface {
	AuthPlugin
	Complete() bool
}

// AuthFactory creates independent mutable state for one authentication plugin use.
type AuthFactory func() AuthPlugin

// AuthOptions supplies pinned keys and per-connector authentication extensions.
// RSAKey is used only for SHA password encryption; untrusted key retrieval is disabled.
type AuthOptions struct {
	RSAKey  *rsa.PublicKey
	Plugins map[string]AuthFactory
	// AllowOldPassword explicitly enables the weak mysql_old_password algorithm.
	// The policy also applies to a custom factory registered under that name.
	AllowOldPassword bool
	// PAM answers dialog prompts under driver-verified TLS. A nil callback uses
	// Credentials.Password once and rejects further prompts. It must honor ctx
	// and be safe for concurrent independent authentications.
	PAM func(context.Context, PAMChallenge) (string, error)
	// PasswordCharset encodes built-in password and PAM answers; default utf8mb4.
	// Custom plugins receive the original Go string and own their encoding.
	PasswordCharset string
	// ServicePrincipalName overrides the GSSAPI server SPN. Native providers use
	// the process identity or credential cache; Credentials.Password is not used.
	// Unix system GSSAPI requires the gssapi build tag.
	ServicePrincipalName string
}
