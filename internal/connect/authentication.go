package connect

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Authenticate negotiates a transport and closes it when authentication fails.
func Authenticate(ctx context.Context, cfg config.Config, raw net.Conn) (result *session.Negotiated, err error) {
	defer func() {
		if result == nil {
			_ = raw.Close()
		}
	}()
	s := &session.Negotiated{Conn: raw, Raw: raw, Packets: wire.NewPacketIO(raw, cfg.Limits.MaxMessageBytes)}
	data, err := s.Packets.ReadMessage(ctx)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[0] == 0xff {
		return nil, command.DecodeServerError(data)
	}
	s.Hello, err = wire.DecodeHandshake(data)
	if err != nil {
		return nil, err
	}
	s.Capabilities, err = ClientCapabilities(cfg, s.Hello.Capabilities)
	if err != nil {
		return nil, err
	}
	collation, err := HandshakeCharset(cfg.Session.Charset)
	if err != nil {
		return nil, err
	}
	response := wire.HandshakeResponse{Capabilities: s.Capabilities, MaxPacketBytes: uint32(min(cfg.Limits.MaxMessageBytes, math.MaxUint32)), Charset: collation}
	if cfg.TLSConfig != nil {
		if err = upgradeTLS(ctx, cfg, s, response); err != nil {
			return nil, err
		}
	}
	credentials, err := ResolveCredentials(ctx, cfg)
	if err != nil {
		return nil, err
	}
	user, err := charset.Encode(cfg.Session.Charset, credentials.User)
	if err != nil {
		return nil, &config.ConfigError{Field: "Credentials.User"}
	}
	response.User = string(user)
	if s.Capabilities&wire.ClientConnectWithDB != 0 {
		database, err := charset.Encode(cfg.Session.Charset, cfg.Database)
		if err != nil {
			return nil, &config.ConfigError{Field: "Database"}
		}
		response.Database = string(database)
	}
	response.Attributes, err = AuthenticationAttributes(cfg, credentials, s.Capabilities)
	if err != nil {
		return nil, err
	}
	response.Plugin = s.Hello.Plugin
	plugin, err := authentication.New(cfg.Auth, response.Plugin)
	if err != nil {
		return nil, err
	}
	use := &authentication.Use{Plugin: plugin}
	defer func() {
		if closeErr := use.Close(); closeErr != nil {
			result, err = nil, errors.Join(err, closeErr)
		}
	}()
	challenge := config.AuthChallenge{Nonce: s.Hello.Challenge, TLSVerified: s.TLSVerified}
	response.Auth, err = authentication.Response(ctx, plugin, response.Plugin, credentials, challenge)
	if err != nil {
		return nil, err
	}
	encoded, err := wire.EncodeHandshakeResponse(response)
	if err != nil {
		return nil, err
	}
	if err = s.Packets.WriteMessage(ctx, encoded); err != nil {
		return nil, err
	}
	if err = FinishAuthentication(ctx, cfg, s, credentials, response.Plugin, use, challenge); err != nil {
		return nil, err
	}
	if err = SelectProtocol(cfg, s); err != nil {
		return nil, err
	}
	s.User, s.ProxyUser = credentials.User, credentials.ProxyUser
	return s, nil
}

// ClientCapabilities validates the greeting and selects client capability declarations.
func ClientCapabilities(cfg config.Config, server uint64) (uint64, error) {
	const required = wire.ClientProtocol41 | wire.ClientSecureConnection
	if server&required != required {
		return 0, fmt.Errorf("%w: protocol 4.1 authentication", dberrors.ErrUnsupported)
	}
	if (len(cfg.ConnectionAttributes) != 0 || cfg.Credentials == nil && cfg.ProxyUser != "") && server&wire.ClientConnectAttrs == 0 {
		return 0, fmt.Errorf("%w: connection attributes", dberrors.ErrUnsupported)
	}
	wanted := required | wire.ClientLongPassword | wire.ClientTransactions | wire.ClientPluginAuth | wire.ClientPluginAuthLenEnc | wire.ClientMultiResults | wire.ClientPSMultiResults | wire.ClientSessionTrack | wire.ClientDeprecateEOF | wire.ClientConnectAttrs
	// Protocol support does not grant access: only an explicit Import supplies a Reader.
	wanted |= wire.ClientLocalFiles
	if cfg.Protocol.Compression != config.FeatureOff {
		if cfg.Protocol.Compression == config.FeatureRequired && server&wire.ClientCompress == 0 {
			return 0, fmt.Errorf("%w: compression", dberrors.ErrUnsupported)
		}
		wanted |= wire.ClientCompress
	}
	if cfg.RowsAffectedMode == config.MatchedRows {
		wanted |= wire.ClientFoundRows
	}
	if cfg.Database != "" && !cfg.CreateDatabaseIfMissing {
		if server&wire.ClientConnectWithDB == 0 {
			return 0, fmt.Errorf("%w: initial database", dberrors.ErrUnsupported)
		}
		wanted |= wire.ClientConnectWithDB
	}
	if cfg.TLSConfig != nil {
		if server&wire.ClientSSL == 0 {
			return 0, dberrors.ErrTLSRequired
		}
		wanted |= wire.ClientSSL
	}
	capabilities := wanted & server
	// This is a client request, independent of the server greeting (Connector/J agrees).
	if cfg.AllowMultiStatements {
		capabilities |= wire.ClientMultiStatements
	}
	if cfg.Interactive {
		capabilities |= wire.ClientInteractive
	}
	// OceanBase requires these declarations even when absent from the server greeting.
	return capabilities | wire.ClientSupportOracle | wire.ClientSupportLOBLocator, nil
}

// HandshakeCharset returns the one-byte collation used during login.
func HandshakeCharset(name string) (byte, error) {
	if id, ok := charset.HandshakeID(name); ok {
		return id, nil
	}
	return 0, fmt.Errorf("%w: handshake charset", dberrors.ErrUnsupported)
}

func upgradeTLS(ctx context.Context, cfg config.Config, s *session.Negotiated, response wire.HandshakeResponse) error {
	encoded, err := wire.EncodeHandshakeResponse(response)
	if err != nil {
		return err
	}
	if err = s.Packets.WriteMessage(ctx, encoded[:32]); err != nil {
		return err
	}
	tlsConfig := configspec.CloneTLSConfig(cfg.TLSConfig)
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName, _, _ = net.SplitHostPort(cfg.Addr)
	}
	// HandshakeContext owns cancellation until packet I/O refreshes the deadlines.
	if err = s.Conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	secure := tls.Client(s.Conn, tlsConfig)
	if err = secure.HandshakeContext(ctx); err != nil {
		if RetryDialError(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
			return &HandshakeTransportError{err}
		}
		return err
	}
	state := secure.ConnectionState()
	if !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		return dberrors.ErrTLSRequired
	}
	if err = s.Packets.ReplaceConn(secure); err != nil {
		return err
	}
	s.Conn, s.TLSVerified = secure, true
	return nil
}

// ResolveCredentials evaluates the provider once and validates the authenticating user.
func ResolveCredentials(ctx context.Context, cfg config.Config) (config.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return config.Credentials{}, err
	}
	credentials := config.Credentials{User: cfg.User, Password: cfg.Password, ProxyUser: cfg.ProxyUser}
	if cfg.Credentials != nil {
		var err error
		credentials, err = cfg.Credentials(ctx)
		if err != nil {
			return config.Credentials{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return config.Credentials{}, err
	}
	if credentials.User == "" || strings.ContainsRune(credentials.User, 0) {
		return config.Credentials{}, &config.ConfigError{Field: "Credentials.User"}
	}
	return credentials, nil
}

// FinishAuthentication completes the bounded plugin conversation and records identity.
func FinishAuthentication(ctx context.Context, cfg config.Config, s *session.Negotiated, credentials config.Credentials, name string, use *authentication.Use, challenge config.AuthChallenge) error {
	expectOK := false
	for range 32 {
		data, err := s.Packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return wire.ErrMalformed
		}
		if conversation, ok := use.Plugin.(config.AuthConversation); ok {
			if conversation.Complete() {
				expectOK = true
			} else {
				challenge.More, challenge.Data = true, data
				response, e := authentication.Response(ctx, use.Plugin, name, credentials, challenge)
				if e != nil {
					return e
				}
				if response != nil {
					if e = s.Packets.WriteMessage(ctx, response); e != nil {
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
			s.OK, err = wire.DecodeOK(data, s.Capabilities)
			if err != nil {
				return err
			}
			s.Mode = config.ModeMySQL
			if s.OK.Status&4 != 0 {
				s.Mode = config.ModeOracle
			}
			if err := checkAuthenticationMode(cfg, s.Mode, credentials); err != nil {
				return err
			}
			s.AuthPlugin, s.AuthChallenge = name, slices.Clone(challenge.Nonce)
			return nil
		case 0xff:
			return command.DecodeServerError(data)
		case 0xfe:
			name, challenge, err = SwitchAuthentication(cfg, s, use, data)
			if err != nil {
				return err
			}
		case 1:
			if name == authentication.PAM {
				return wire.ErrMalformed
			}
			if name == authentication.CachingSHA2 && len(data) == 2 && data[1] == 3 {
				expectOK = true
				continue
			}
			challenge.More, challenge.Data = true, data[1:]
		default:
			if name != authentication.PAM {
				return wire.ErrMalformed
			}
			challenge.More, challenge.Data = true, data
		}
		response, err := authentication.Response(ctx, use.Plugin, name, credentials, challenge)
		if err != nil {
			return err
		}
		if err = s.Packets.WriteMessage(ctx, response); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: too many authentication exchanges", dberrors.ErrUnsupported)
}

func checkAuthenticationMode(cfg config.Config, mode config.Mode, credentials config.Credentials) error {
	if cfg.Mode != config.ModeAuto && mode != cfg.Mode {
		return dberrors.ErrModeMismatch
	}
	if mode == config.ModeOracle && cfg.RowsAffectedMode == config.ChangedRows {
		return &config.ConfigError{Field: "RowsAffectedMode"}
	}
	if credentials.ProxyUser != "" && mode != config.ModeOracle {
		return fmt.Errorf("%w: proxy login requires Oracle compatibility mode", dberrors.ErrUnsupported)
	}
	return nil
}

// SwitchAuthentication closes the previous plugin before adopting the requested plugin.
func SwitchAuthentication(cfg config.Config, s *session.Negotiated, use *authentication.Use, data []byte) (string, config.AuthChallenge, error) {
	name, nonce := authentication.Old, s.Hello.Challenge
	if len(data) > 1 {
		end := bytes.IndexByte(data[1:], 0)
		if end <= 0 || end > 128 {
			return "", config.AuthChallenge{}, wire.ErrMalformed
		}
		name, nonce = string(data[1:1+end]), data[2+end:]
	}
	if err := use.Close(); err != nil {
		return "", config.AuthChallenge{}, err
	}
	plugin, err := authentication.New(cfg.Auth, name)
	if err != nil {
		return "", config.AuthChallenge{}, err
	}
	use.Plugin = plugin
	length := 0
	switch name {
	case authentication.Ed25519:
		length = 32
	case authentication.Native, authentication.CachingSHA2, authentication.SHA256:
		length = 20
	}
	_, conversation := plugin.(config.AuthConversation)
	if len(data) > 1 && name != authentication.PAM && !conversation && (length == 0 || len(nonce) == length+1) {
		nonce = bytes.TrimSuffix(nonce, []byte{0})
	}
	return name, config.AuthChallenge{Nonce: nonce, TLSVerified: s.TLSVerified}, nil
}
