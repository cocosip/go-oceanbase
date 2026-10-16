package oceanbase

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Authentication and mode errors are safe to inspect with errors.Is.
var (
	ErrTLSRequired  = errors.New("oceanbase: authentication requires verified TLS")
	ErrModeMismatch = errors.New("oceanbase: compatibility mode mismatch")
)

const (
	authNative      = "mysql_native_password"
	authCachingSHA2 = "caching_sha2_password"
	authSHA256      = "sha256_password"
	authClear       = "mysql_clear_password"
)

// Credentials are obtained for each new authentication, never during a transaction.
type Credentials struct{ User, Password string }

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
type AuthPlugin interface {
	Security(AuthChallenge) AuthSecurity
	Respond(context.Context, Credentials, AuthChallenge) ([]byte, error)
}

// AuthFactory creates independent mutable state for one authentication plugin use.
type AuthFactory func() AuthPlugin

// AuthOptions supplies pinned keys and per-connector authentication extensions.
// RSAKey is used only for SHA password encryption; untrusted key retrieval is disabled.
type AuthOptions struct {
	RSAKey  *rsa.PublicKey
	Plugins map[string]AuthFactory
}

type handshakeSession struct {
	conn         net.Conn
	raw          net.Conn
	packets      *wire.PacketIO
	hello        wire.Handshake
	ok           wire.OK
	mode         Mode
	capabilities uint64
	tlsVerified  bool
	user         string
	obVersion    string
}

func (c *connector) authenticate(ctx context.Context, raw net.Conn) (result *handshakeSession, err error) {
	defer func() {
		if result == nil {
			_ = raw.Close()
		}
	}()
	s := &handshakeSession{conn: raw, raw: raw, packets: wire.NewPacketIO(raw, c.cfg.Limits.MaxMessageBytes)}
	data, err := s.packets.ReadMessage(ctx)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[0] == 0xff {
		return nil, decodeServerError(data)
	}
	s.hello, err = wire.DecodeHandshake(data)
	if err != nil {
		return nil, err
	}
	s.capabilities, err = c.clientCapabilities(s.hello.Capabilities)
	if err != nil {
		return nil, err
	}
	charset, err := handshakeCharset(c.cfg.Session.Charset)
	if err != nil {
		return nil, err
	}
	response := wire.HandshakeResponse{Capabilities: s.capabilities, MaxPacketBytes: uint32(min(c.cfg.Limits.MaxMessageBytes, math.MaxUint32)), Charset: charset, Database: c.cfg.Database}
	if c.cfg.TLSConfig != nil {
		if err = c.upgradeTLS(ctx, s, response); err != nil {
			return nil, err
		}
	}
	credentials, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	response.User, response.Plugin = credentials.User, s.hello.Plugin
	plugin, err := c.newAuthPlugin(response.Plugin)
	if err != nil {
		return nil, err
	}
	challenge := AuthChallenge{Nonce: s.hello.Challenge, TLSVerified: s.tlsVerified}
	response.Auth, err = authResponse(ctx, plugin, response.Plugin, credentials, challenge)
	if err != nil {
		return nil, err
	}
	encoded, err := wire.EncodeHandshakeResponse(response)
	if err != nil {
		return nil, err
	}
	if err = s.packets.WriteMessage(ctx, encoded); err != nil {
		return nil, err
	}
	if err = c.finishAuthentication(ctx, s, credentials, response.Plugin, plugin, challenge); err != nil {
		return nil, err
	}
	s.user = credentials.User
	return s, nil
}

func (c *connector) clientCapabilities(server uint64) (uint64, error) {
	const required = wire.ClientProtocol41 | wire.ClientSecureConnection
	if server&required != required {
		return 0, fmt.Errorf("%w: protocol 4.1 authentication", ErrUnsupported)
	}
	wanted := required | wire.ClientLongPassword | wire.ClientTransactions | wire.ClientPluginAuth | wire.ClientPluginAuthLenEnc | wire.ClientMultiResults | wire.ClientSessionTrack | wire.ClientDeprecateEOF
	if c.cfg.AllowMultiStatements {
		wanted |= wire.ClientMultiStatements
	}
	if c.cfg.RowsAffectedMode == MatchedRows {
		wanted |= wire.ClientFoundRows
	}
	if c.cfg.Database != "" {
		if server&wire.ClientConnectWithDB == 0 {
			return 0, fmt.Errorf("%w: initial database", ErrUnsupported)
		}
		wanted |= wire.ClientConnectWithDB
	}
	if c.cfg.TLSConfig != nil {
		if server&wire.ClientSSL == 0 {
			return 0, ErrTLSRequired
		}
		wanted |= wire.ClientSSL
	}
	// OceanBase requires this declaration even when absent from the server greeting.
	return wanted&server | wire.ClientSupportOracle, nil
}

func handshakeCharset(name string) (byte, error) {
	switch strings.ToLower(name) {
	case "utf8mb4":
		return 45, nil
	case "utf8", "utf8mb3":
		return 33, nil
	case "latin1":
		return 8, nil
	default:
		return 0, fmt.Errorf("%w: handshake charset", ErrUnsupported)
	}
}

func (c *connector) upgradeTLS(ctx context.Context, s *handshakeSession, response wire.HandshakeResponse) error {
	encoded, err := wire.EncodeHandshakeResponse(response)
	if err != nil {
		return err
	}
	if err = s.packets.WriteMessage(ctx, encoded[:32]); err != nil {
		return err
	}
	cfg := cloneTLSConfig(c.cfg.TLSConfig)
	if cfg.ServerName == "" {
		cfg.ServerName, _, _ = net.SplitHostPort(c.cfg.Addr)
	}
	secure := tls.Client(s.conn, cfg)
	if err = secure.HandshakeContext(ctx); err != nil {
		return err
	}
	state := secure.ConnectionState()
	if !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		return ErrTLSRequired
	}
	if err = s.packets.ReplaceConn(secure); err != nil {
		return err
	}
	s.conn, s.tlsVerified = secure, true
	return nil
}

func (c *connector) credentials(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	credentials := Credentials{User: c.cfg.User, Password: c.cfg.Password}
	if c.cfg.Credentials != nil {
		var err error
		credentials, err = c.cfg.Credentials(ctx)
		if err != nil {
			return Credentials{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	if credentials.User == "" || strings.ContainsRune(credentials.User, 0) {
		return Credentials{}, &ConfigError{Field: "Credentials.User"}
	}
	return credentials, nil
}

func (c *connector) newAuthPlugin(name string) (AuthPlugin, error) {
	if factory, ok := c.cfg.Auth.Plugins[name]; ok {
		plugin := factory()
		if plugin == nil {
			return nil, fmt.Errorf("%w: nil authentication plugin", ErrUnsupported)
		}
		return plugin, nil
	}
	switch name {
	case authNative, authCachingSHA2, authSHA256, authClear:
		return builtinAuth{name: name, key: c.cfg.Auth.RSAKey}, nil
	default:
		return nil, fmt.Errorf("%w: authentication plugin", ErrUnsupported)
	}
}

func copyChallenge(c AuthChallenge) AuthChallenge {
	c.Nonce, c.Data = slices.Clone(c.Nonce), slices.Clone(c.Data)
	return c
}

func authResponse(ctx context.Context, plugin AuthPlugin, name string, credentials Credentials, c AuthChallenge) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	security := plugin.Security(copyChallenge(c))
	if security > AuthRequireTLS {
		return nil, fmt.Errorf("%w: authentication security level", ErrUnsupported)
	}
	if (name == authClear || name == "dialog" || security == AuthRequireTLS) && !c.TLSVerified {
		return nil, ErrTLSRequired
	}
	response, err := plugin.Respond(ctx, credentials, copyChallenge(c))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *connector) finishAuthentication(ctx context.Context, s *handshakeSession, credentials Credentials, name string, plugin AuthPlugin, challenge AuthChallenge) error {
	expectOK := false
	for range 32 {
		data, err := s.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return wire.ErrMalformed
		}
		if expectOK && data[0] != 0 && data[0] != 0xff {
			return wire.ErrMalformed
		}
		switch data[0] {
		case 0:
			s.ok, err = wire.DecodeOK(data, s.capabilities)
			if err != nil {
				return err
			}
			s.mode = ModeMySQL
			if s.ok.Status&4 != 0 {
				s.mode = ModeOracle
			}
			if c.cfg.Mode != ModeAuto && s.mode != c.cfg.Mode {
				return ErrModeMismatch
			}
			if s.mode == ModeOracle && c.cfg.RowsAffectedMode == ChangedRows {
				return &ConfigError{Field: "RowsAffectedMode"}
			}
			return nil
		case 0xff:
			return decodeServerError(data)
		case 0xfe:
			end := bytes.IndexByte(data[1:], 0)
			if end <= 0 || end > 128 {
				return wire.ErrMalformed
			}
			name = string(data[1 : 1+end])
			plugin, err = c.newAuthPlugin(name)
			if err != nil {
				return err
			}
			challenge = AuthChallenge{Nonce: bytes.TrimSuffix(data[2+end:], []byte{0}), TLSVerified: s.tlsVerified}
		case 1:
			if name == authCachingSHA2 && len(data) == 2 && data[1] == 3 {
				expectOK = true
				continue
			}
			challenge.More, challenge.Data = true, data[1:]
		default:
			return wire.ErrMalformed
		}
		response, err := authResponse(ctx, plugin, name, credentials, challenge)
		if err != nil {
			return err
		}
		if err = s.packets.WriteMessage(ctx, response); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: too many authentication exchanges", ErrUnsupported)
}

type builtinAuth struct {
	name string
	key  *rsa.PublicKey
}

func (p builtinAuth) Security(c AuthChallenge) AuthSecurity {
	if p.name == authClear || ((p.name == authSHA256 || (p.name == authCachingSHA2 && c.More)) && p.key == nil) {
		return AuthRequireTLS
	}
	return AuthChallengeResponse
}

func (p builtinAuth) Respond(_ context.Context, credentials Credentials, c AuthChallenge) ([]byte, error) {
	switch p.name {
	case authNative:
		if c.More {
			return nil, wire.ErrMalformed
		}
		return auth.Native(credentials.Password, c.Nonce)
	case authCachingSHA2:
		if !c.More {
			return auth.CachingSHA2(credentials.Password, c.Nonce)
		}
		if len(c.Data) != 1 || c.Data[0] != 4 {
			return nil, wire.ErrMalformed
		}
	case authSHA256:
		if c.More {
			return nil, wire.ErrMalformed
		}
	case authClear:
		if c.More {
			return nil, wire.ErrMalformed
		}
	}
	if c.TLSVerified {
		return auth.Clear(credentials.Password)
	}
	return auth.EncryptPassword(credentials.Password, c.Nonce, p.key)
}
