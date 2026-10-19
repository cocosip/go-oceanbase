package oceanbase

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
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
	authOld         = "mysql_old_password"
	authEd25519     = "client_ed25519"
	authPAM         = "dialog"
	authGSSAPI      = "auth_gssapi_client"
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

type handshakeSession struct {
	conn          net.Conn
	raw           net.Conn
	packets       *wire.PacketIO
	hello         wire.Handshake
	ok            wire.OK
	mode          Mode
	capabilities  uint64
	tlsVerified   bool
	user          string
	proxyUser     string
	obVersion     string
	newMetadata   bool
	authPlugin    string
	authChallenge []byte
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
	collation, err := handshakeCharset(c.cfg.Session.Charset)
	if err != nil {
		return nil, err
	}
	response := wire.HandshakeResponse{Capabilities: s.capabilities, MaxPacketBytes: uint32(min(c.cfg.Limits.MaxMessageBytes, math.MaxUint32)), Charset: collation}
	if c.cfg.TLSConfig != nil {
		if err = c.upgradeTLS(ctx, s, response); err != nil {
			return nil, err
		}
	}
	credentials, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	user, err := charset.Encode(c.cfg.Session.Charset, credentials.User)
	if err != nil {
		return nil, &ConfigError{Field: "Credentials.User"}
	}
	response.User = string(user)
	if s.capabilities&wire.ClientConnectWithDB != 0 {
		database, err := charset.Encode(c.cfg.Session.Charset, c.cfg.Database)
		if err != nil {
			return nil, &ConfigError{Field: configDatabaseField}
		}
		response.Database = string(database)
	}
	response.Attributes, err = c.authenticationAttributes(credentials, s.capabilities)
	if err != nil {
		return nil, err
	}
	response.Plugin = s.hello.Plugin
	plugin, err := c.newAuthPlugin(response.Plugin)
	if err != nil {
		return nil, err
	}
	use := &authPluginUse{plugin: plugin}
	defer func() {
		if closeErr := use.close(); closeErr != nil {
			result, err = nil, errors.Join(err, closeErr)
		}
	}()
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
	if err = c.finishAuthentication(ctx, s, credentials, response.Plugin, use, challenge); err != nil {
		return nil, err
	}
	if err = c.selectProtocol(s); err != nil {
		return nil, err
	}
	s.user, s.proxyUser = credentials.User, credentials.ProxyUser
	return s, nil
}

func (c *connector) clientCapabilities(server uint64) (uint64, error) {
	const required = wire.ClientProtocol41 | wire.ClientSecureConnection
	if server&required != required {
		return 0, fmt.Errorf("%w: protocol 4.1 authentication", ErrUnsupported)
	}
	if (len(c.cfg.ConnectionAttributes) != 0 || c.cfg.Credentials == nil && c.cfg.ProxyUser != "") && server&wire.ClientConnectAttrs == 0 {
		return 0, fmt.Errorf("%w: connection attributes", ErrUnsupported)
	}
	wanted := required | wire.ClientLongPassword | wire.ClientTransactions | wire.ClientPluginAuth | wire.ClientPluginAuthLenEnc | wire.ClientMultiResults | wire.ClientPSMultiResults | wire.ClientSessionTrack | wire.ClientDeprecateEOF | wire.ClientConnectAttrs
	// Protocol support does not grant access: only an explicit Import supplies a Reader.
	wanted |= wire.ClientLocalFiles
	if c.cfg.Protocol.Compression != FeatureOff {
		if c.cfg.Protocol.Compression == FeatureRequired && server&wire.ClientCompress == 0 {
			return 0, fmt.Errorf("%w: compression", ErrUnsupported)
		}
		wanted |= wire.ClientCompress
	}
	if c.cfg.RowsAffectedMode == MatchedRows {
		wanted |= wire.ClientFoundRows
	}
	if c.cfg.Database != "" && !c.cfg.CreateDatabaseIfMissing {
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
	capabilities := wanted & server
	// This is a client request, independent of the server greeting (Connector/J agrees).
	if c.cfg.AllowMultiStatements {
		capabilities |= wire.ClientMultiStatements
	}
	if c.cfg.Interactive {
		capabilities |= wire.ClientInteractive
	}
	// OceanBase requires these declarations even when absent from the server greeting.
	return capabilities | wire.ClientSupportOracle | wire.ClientSupportLOBLocator, nil
}

func handshakeCharset(name string) (byte, error) {
	switch strings.ToLower(name) {
	case charsetUTF8MB4:
		return 45, nil
	case charsetUTF8, charsetUTF8MB3Alias:
		return 33, nil
	case "latin1":
		return 8, nil
	case "gbk":
		return 28, nil
	case "gb18030":
		return 248, nil
	// OceanBase ob_charset.h: HKSCS_BIN, HKSCS31_BIN and GB18030_2022_PINYIN_CI.
	case "hkscs":
		return 152, nil
	case "hkscs31":
		return 153, nil
	case "gb18030_2022":
		return 217, nil
	case "ascii":
		return 11, nil
	case charsetBinary:
		return 63, nil
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
	// HandshakeContext owns cancellation until packet I/O refreshes the deadlines.
	if err = s.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	secure := tls.Client(s.conn, cfg)
	if err = secure.HandshakeContext(ctx); err != nil {
		if retryDialError(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
			return &handshakeTransportError{err}
		}
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
	credentials := Credentials{User: c.cfg.User, Password: c.cfg.Password, ProxyUser: c.cfg.ProxyUser}
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
	if name == authOld && !c.cfg.Auth.AllowOldPassword {
		return nil, fmt.Errorf("%w: legacy password authentication is disabled", ErrUnsupported)
	}
	if factory, ok := c.cfg.Auth.Plugins[name]; ok {
		plugin := factory()
		if nilAuthPlugin(plugin) {
			return nil, fmt.Errorf("%w: nil authentication plugin", ErrUnsupported)
		}
		return plugin, nil
	}
	switch name {
	case authGSSAPI:
		return &gssAuth{principal: c.cfg.Auth.ServicePrincipalName}, nil
	case authPAM:
		return &pamAuth{respond: c.cfg.Auth.PAM, charset: c.cfg.Auth.PasswordCharset}, nil
	case authNative, authCachingSHA2, authSHA256, authClear, authOld, authEd25519:
		return builtinAuth{name: name, key: c.cfg.Auth.RSAKey, charset: c.cfg.Auth.PasswordCharset}, nil
	default:
		return nil, fmt.Errorf("%w: authentication plugin", ErrUnsupported)
	}
}

func nilAuthPlugin(plugin AuthPlugin) bool {
	if plugin == nil {
		return true
	}
	v := reflect.ValueOf(plugin)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
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
	if (name == authClear || name == authPAM || security == AuthRequireTLS) && !c.TLSVerified {
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

type authPluginUse struct{ plugin AuthPlugin }

func (u *authPluginUse) close() error {
	plugin := u.plugin
	u.plugin = nil
	if closer, ok := plugin.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (c *connector) finishAuthentication(ctx context.Context, s *handshakeSession, credentials Credentials, name string, use *authPluginUse, challenge AuthChallenge) error {
	expectOK := false
	for range 32 {
		data, err := s.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return wire.ErrMalformed
		}
		if conversation, ok := use.plugin.(AuthConversation); ok {
			if conversation.Complete() {
				expectOK = true
			} else {
				challenge.More, challenge.Data = true, data
				response, e := authResponse(ctx, use.plugin, name, credentials, challenge)
				if e != nil {
					return e
				}
				if response != nil {
					if e = s.packets.WriteMessage(ctx, response); e != nil {
						return e
					}
				}
				continue
			}
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
			if err := c.checkAuthenticationMode(s.mode, credentials); err != nil {
				return err
			}
			s.authPlugin, s.authChallenge = name, slices.Clone(challenge.Nonce)
			return nil
		case 0xff:
			return decodeServerError(data)
		case 0xfe:
			name, challenge, err = c.switchAuthentication(s, use, data)
			if err != nil {
				return err
			}
		case 1:
			if name == authPAM {
				return wire.ErrMalformed
			}
			if name == authCachingSHA2 && len(data) == 2 && data[1] == 3 {
				expectOK = true
				continue
			}
			challenge.More, challenge.Data = true, data[1:]
		default:
			if name != authPAM {
				return wire.ErrMalformed
			}
			challenge.More, challenge.Data = true, data
		}
		response, err := authResponse(ctx, use.plugin, name, credentials, challenge)
		if err != nil {
			return err
		}
		if err = s.packets.WriteMessage(ctx, response); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: too many authentication exchanges", ErrUnsupported)
}

func (c *connector) checkAuthenticationMode(mode Mode, credentials Credentials) error {
	if c.cfg.Mode != ModeAuto && mode != c.cfg.Mode {
		return ErrModeMismatch
	}
	if mode == ModeOracle && c.cfg.RowsAffectedMode == ChangedRows {
		return &ConfigError{Field: "RowsAffectedMode"}
	}
	if credentials.ProxyUser != "" && mode != ModeOracle {
		return fmt.Errorf("%w: proxy login requires Oracle compatibility mode", ErrUnsupported)
	}
	return nil
}

func (c *connector) switchAuthentication(s *handshakeSession, use *authPluginUse, data []byte) (string, AuthChallenge, error) {
	name, nonce := authOld, s.hello.Challenge
	if len(data) > 1 {
		end := bytes.IndexByte(data[1:], 0)
		if end <= 0 || end > 128 {
			return "", AuthChallenge{}, wire.ErrMalformed
		}
		name, nonce = string(data[1:1+end]), data[2+end:]
	}
	if err := use.close(); err != nil {
		return "", AuthChallenge{}, err
	}
	plugin, err := c.newAuthPlugin(name)
	if err != nil {
		return "", AuthChallenge{}, err
	}
	use.plugin = plugin
	length := 0
	switch name {
	case authEd25519:
		length = 32
	case authNative, authCachingSHA2, authSHA256:
		length = 20
	}
	_, conversation := plugin.(AuthConversation)
	if len(data) > 1 && name != authPAM && !conversation && (length == 0 || len(nonce) == length+1) {
		nonce = bytes.TrimSuffix(nonce, []byte{0})
	}
	return name, AuthChallenge{Nonce: nonce, TLSVerified: s.tlsVerified}, nil
}

type builtinAuth struct {
	name    string
	key     *rsa.PublicKey
	charset string
}

func (p builtinAuth) Security(c AuthChallenge) AuthSecurity {
	if p.name == authClear || ((p.name == authSHA256 || (p.name == authCachingSHA2 && c.More)) && p.key == nil) {
		return AuthRequireTLS
	}
	return AuthChallengeResponse
}

func (p builtinAuth) Respond(_ context.Context, credentials Credentials, c AuthChallenge) ([]byte, error) {
	password, err := charset.Encode(p.charset, credentials.Password)
	if err != nil {
		return nil, err
	}
	credentials.Password = string(password)
	switch p.name {
	case authOld, authEd25519:
		if c.More {
			return nil, wire.ErrMalformed
		}
		if p.name == authOld {
			return auth.OldPassword(credentials.Password, c.Nonce)
		}
		return auth.Ed25519(credentials.Password, c.Nonce)
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
