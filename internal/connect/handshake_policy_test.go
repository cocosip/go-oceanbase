package connect

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPacketNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		options     config.ProtocolOptions
		compression bool
		offered     bool
		vars        map[string]string
		framed      bool
		metadata    bool
		unsupported bool
	}{
		{name: testStandard, options: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff}},
		{name: "default-with-offer", compression: true},
		{name: "auto-no-offer", options: config.ProtocolOptions{Compression: config.FeatureAuto}},
		{name: "compressed", options: config.ProtocolOptions{Compression: config.FeatureAuto}, compression: true, offered: true, framed: true},
		{name: "compression-required", options: config.ProtocolOptions{Compression: config.FeatureRequired}, compression: true, offered: true, framed: true},
		{name: "ob20-with-optional-compression", options: config.ProtocolOptions{Compression: config.FeatureAuto}, compression: true, offered: true, vars: map[string]string{testProxyCapability: "33024"}, framed: true},
		{name: testOB20, vars: map[string]string{testProxyCapability: "33024"}, framed: true},
		{name: "ob20-required", options: config.ProtocolOptions{OB20: config.FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, framed: true},
		{name: "ob20-missing", options: config.ProtocolOptions{OB20: config.FeatureRequired}, unsupported: true},
		{name: "compress-missing", options: config.ProtocolOptions{Compression: config.FeatureRequired}, unsupported: true},
		{name: "metadata-missing", options: config.ProtocolOptions{NewMetadata: config.FeatureRequired}, unsupported: true},
		{name: "metadata-required", options: config.ProtocolOptions{NewMetadata: config.FeatureRequired}, vars: map[string]string{"__ob_client_capability_flag": "2"}, metadata: true},
		{name: "checksum-missing", options: config.ProtocolOptions{Checksum: config.FeatureRequired}, unsupported: true},
		{name: "checksum-required", options: config.ProtocolOptions{Checksum: config.FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, framed: true},
		{name: "off-unsolicited", options: config.ProtocolOptions{OB20: config.FeatureOff}, vars: map[string]string{testProxyCapability: "256"}, unsupported: true},
		{name: "trace-required", options: config.ProtocolOptions{FullLinkTrace: config.FeatureRequired}, vars: map[string]string{testProxyCapability: "16640"}, framed: true},
		{name: "trace-missing", options: config.ProtocolOptions{FullLinkTrace: config.FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, unsupported: true},
		{name: "trace-off", options: config.ProtocolOptions{FullLinkTrace: config.FeatureOff}, vars: map[string]string{testProxyCapability: "16640"}, unsupported: true},
		{name: "trace-without-ob20", vars: map[string]string{testProxyCapability: "16384"}, unsupported: true},
		{name: "invalid-ack", vars: map[string]string{testProxyCapability: "not-a-number"}, unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := runAuthTest(t, config.Config{User: testUser, Protocol: tc.options}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, protocolGreeting(tc.compression)); err != nil {
					t.Error(err)
					return
				}
				request, err := p.ReadMessage(ctx)
				if err != nil {
					return
				}
				offered := binary.LittleEndian.Uint32(request)&32 != 0
				if offered != tc.offered {
					t.Error("compression handshake flag")
				}
				_ = p.WriteMessage(ctx, protocolOK(2, tc.vars))
			})
			if tc.unsupported {
				if err == nil {
					t.Fatal("unsupported negotiation succeeded")
				}
				return
			}
			if err != nil || s.Packets.Framed() != tc.framed || s.NewMetadata != tc.metadata {
				t.Fatal(s, err)
			}
		})
	}
}

func TestProxyChecksumPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  config.FeatureMode
		ack     uint64
		wantErr bool
	}{
		{config.FeatureAuto, 0, false}, {config.FeatureOff, 0, false}, {config.FeatureRequired, 72, false},
		{config.FeatureRequired, 8, true}, {config.FeatureRequired, 0, true}, {config.FeatureOff, 72, true},
	} {
		cfg := config.Config{Protocol: config.ProtocolOptions{OB20: config.FeatureOff, Compression: config.FeatureOff, ProxyChecksum: tc.policy}}
		attrs := ProtocolAttributes(cfg)
		proxy, _ := strconv.ParseUint(attrs["__proxy_capability_flag"], 10, 64)
		if (proxy&72 == 72) != (tc.policy != config.FeatureOff) {
			t.Fatal("proxy checksum declaration", attrs)
		}
		s := &session.Negotiated{Packets: wire.NewPacketIO(nil, 64), Capabilities: wire.ClientConnectAttrs}
		ok, err := wire.DecodeOK(protocolOK(2, map[string]string{testProxyCapability: strconv.FormatUint(tc.ack, 10)}), wire.ClientSessionTrack|wire.ClientProtocol41)
		if err != nil {
			t.Fatal(err)
		}
		s.OK = ok
		err = SelectProtocol(cfg, s)
		if (err != nil) != tc.wantErr {
			t.Fatal(tc, err)
		}
		if err != nil && !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := newConnector(config.Config{User: testUser, Protocol: config.ProtocolOptions{ProxyChecksum: 255}}); err == nil {
		t.Fatal("invalid proxy policy")
	}
	cfg, err := config.ParseDSN("oceanbase://u@localhost/?proxyChecksum=required&ob20=off")
	if err != nil || cfg.Protocol.ProxyChecksum != config.FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
}

func TestConnectionAttributesHandshake(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, interactive := range []bool{false, true} {
			for _, tc := range []struct {
				charset, key, value string
				collation           byte
			}{
				{testCharsetUTF8MB4, "应用", "测试", 45},
				{localFilenameCharset, "\xd3\xa6\xd3\xc3", testAttributeGBKValue, 28},
			} {
				t.Run(fmt.Sprintf("%d/%t/%s", mode, interactive, tc.charset), func(t *testing.T) {
					cfg := config.Config{User: testUser, Mode: mode, Interactive: interactive, Session: config.SessionOptions{Charset: tc.charset}, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue, "应用": "测试", testAttributeEmpty: ""}, Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff, ProxyChecksum: config.FeatureOff, NewMetadata: config.FeatureOff}}
					s, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
						if !sendPackets(ctx, t, p, protocolGreeting(false)) {
							return
						}
						caps := uint64(0x28ba8203)
						if interactive {
							caps |= 1 << 10
						}
						want, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: caps, MaxPacketBytes: 64 << 20, Charset: tc.collation, User: testUser, Plugin: authentication.Native, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: testAttributeValue, tc.key: tc.value, testAttributeEmpty: "", testClientAttributeCapability: "1", testProxyRequestCapability: "1048576"}})
						if err != nil {
							t.Error(err)
							return
						}
						got, err := p.ReadMessage(ctx)
						if err != nil || !bytes.Equal(got, want) {
							t.Errorf("handshake mismatch: got %x want %x (%v)", got, want, err)
							return
						}
						status := byte(2)
						if mode == config.ModeOracle {
							status = 6
						}
						sendPackets(ctx, t, p, commandPacket(0, 0, status))
					})
					if err != nil || s.Mode != mode {
						t.Fatal("attribute handshake failed", err)
					}
				})
			}
		}
	}
}

func TestConnectionAttributesMissingCapability(t *testing.T) {
	calls := 0
	_, err := runAuthTest(t, config.Config{Credentials: func(context.Context) (config.Credentials, error) {
		calls++
		return config.Credentials{User: testUser}, nil
	}, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue}}, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authentication.Native, false)) {
			return
		}
		if got, err := p.ReadMessage(ctx); err == nil || len(got) != 0 {
			t.Error("unsupported explicit attributes sent authentication bytes")
		}
	})
	if !errors.Is(err, dberrors.ErrUnsupported) || calls != 0 {
		t.Fatal("explicit attributes silently dropped or invoked credentials", err, calls)
	}
}

func TestInteractiveCapability(t *testing.T) {
	for _, offered := range []uint64{0, 1 << 10} {
		for _, interactive := range []bool{false, true} {
			cfg := config.Config{Interactive: interactive}
			caps, err := ClientCapabilities(cfg, wire.ClientProtocol41|wire.ClientSecureConnection|offered)
			if err != nil || (caps&(1<<10) != 0) != interactive {
				t.Fatal("interactive client request depends on server offer", caps, err)
			}
		}
	}
	for _, value := range []string{dsnTrue, dsnFalse} {
		cfg, err := config.ParseDSN("oceanbase://app@localhost/?interactive=" + value)
		if err != nil || cfg.Interactive != (value == dsnTrue) {
			t.Fatal("interactive DSN value ignored", err)
		}
	}
}

func TestProxyUserHandshake(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		cfg := config.Config{User: testUser, ProxyUser: testProxyUser, Mode: config.ModeOracle,
			Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff, NewMetadata: config.FeatureOff, ProxyChecksum: config.FeatureOff}}
		if dynamic {
			cfg.ProxyUser = "overridden"
			cfg.Credentials = func(context.Context) (config.Credentials, error) {
				return config.Credentials{User: testUser, ProxyUser: testProxyUser}, nil
			}
		}
		s, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
			if !sendPackets(ctx, t, p, protocolGreeting(false)) {
				return
			}
			want, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{
				Capabilities: 0x28ba8203, MaxPacketBytes: 64 << 20, Charset: 45, User: testUser, Plugin: authentication.Native,
				Attributes: map[string]string{testClientAttributeCapability: "1", testProxyUserAttribute: testProxyUser,
					testDriverDialectAttribute: testDriverDialectValue, testProxyRequestCapability: "1048576"},
			})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := p.ReadMessage(ctx)
			if err != nil || !bytes.Equal(got, want) {
				t.Error("proxy target not sent independently of authenticating user", err)
				return
			}
			sendPackets(ctx, t, p, commandPacket(0, 0, 6))
		})
		if err != nil || s.User != testUser || s.ProxyUser != testProxyUser {
			t.Fatal("proxy authentication identity not preserved", err)
		}
	}
}

func TestProxyUserRejection(t *testing.T) {
	for _, path := range []string{"missing-capability", "dynamic-capability", "dynamic-encoding", "dynamic-mysql", testProxyDetectedMySQL, serverErrorPath} {
		t.Run(path, func(t *testing.T) {
			cfg := config.Config{User: testUser, ProxyUser: testProxyUser}
			calls := 0
			if strings.HasPrefix(path, "dynamic-") {
				cfg.ProxyUser = ""
				cfg.Credentials = func(context.Context) (config.Credentials, error) {
					calls++
					proxy := testProxyUser
					if path == "dynamic-encoding" {
						proxy = "bad\xff"
					}
					return config.Credentials{User: testUser, ProxyUser: proxy}, nil
				}
			}
			if path == "dynamic-mysql" {
				cfg.Mode = config.ModeMySQL
			}
			_, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
				greeting := protocolGreeting(false)
				if path == "missing-capability" || path == "dynamic-capability" {
					greeting = authGreeting(authentication.Native, false)
				}
				if !sendPackets(ctx, t, p, greeting) {
					return
				}
				if path == testProxyDetectedMySQL || path == serverErrorPath {
					if !readAuthResponse(ctx, t, p) {
						return
					}
					response := commandPacket(0, 0, 2)
					if path == serverErrorPath {
						response = []byte{255, 211, 4, '#', '0', 'A', '0', '0', '0', 'x'}
					}
					if !sendPackets(ctx, t, p, response) {
						return
					}
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("rejected proxy login continued or fell back")
				}
			})
			if err == nil {
				t.Fatal("unsupported proxy identity accepted")
			}
			if strings.Contains(path, "capability") || path == testProxyDetectedMySQL {
				if !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal("capability or mode failure hidden", err)
				}
			}
			if path == serverErrorPath {
				var server *dberrors.Error
				if !errors.As(err, &server) || server.Code != 1235 {
					t.Fatal("proxy login server error hidden", err)
				}
			}
			if strings.HasPrefix(path, "dynamic-") && calls != 1 {
				t.Fatal("credential provider repeated")
			}
		})
	}
}

func TestProxyUserCallbackClearsTarget(t *testing.T) {
	s, err := runAuthTest(t, config.Config{User: testUser, ProxyUser: testProxyUser, Credentials: func(context.Context) (config.Credentials, error) {
		return config.Credentials{User: testUser}, nil
	}}, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authentication.Native, false)) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || bytes.Contains(response, []byte(testProxyUserAttribute)) {
			t.Error("cleared proxy target was still sent", err)
			return
		}
		sendPackets(ctx, t, p, commandPacket(0, 0, 2))
	})
	if err != nil || s.ProxyUser != "" || s.Mode != config.ModeMySQL {
		t.Fatal("credential callback could not clear static proxy target", err)
	}
}

func TestProxyUserVerifiedTLS(t *testing.T) {
	clientCfg, serverCfg := testTLSConfig(t)
	s, err := runAuthConnTest(t, config.Config{User: testUser, ProxyUser: testProxyUser, Mode: config.ModeOracle, TLSConfig: clientCfg}, nil,
		func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
			greeting := protocolGreeting(false)
			start := bytes.IndexByte(greeting[1:], 0) + 2 + 4 + 8 + 1
			greeting[start+1] |= 8
			if !sendPackets(ctx, t, p, greeting) {
				return
			}
			request, err := p.ReadMessage(ctx)
			if err != nil || len(request) != 32 || bytes.Contains(request, []byte(testProxyUser)) {
				t.Error("proxy identity leaked into SSLRequest", err)
				return
			}
			secure := tls.Server(raw, serverCfg)
			if err := secure.HandshakeContext(ctx); err != nil {
				t.Error(err)
				return
			}
			if err := p.ReplaceConn(authPeerConn{Conn: secure}); err != nil {
				t.Error(err)
				return
			}
			response, err := p.ReadMessage(ctx)
			if err != nil || !bytes.Contains(response, []byte(testProxyUserAttribute)) || !bytes.Contains(response, []byte(testProxyUser)) {
				t.Error("proxy target missing from encrypted authentication", err)
				return
			}
			sendPackets(ctx, t, p, commandPacket(0, 0, 6))
		})
	if err != nil || !s.TLSVerified || s.ProxyUser != testProxyUser {
		t.Fatal("verified TLS proxy authentication rejected", err)
	}
}

func TestProxyUserEncodedBudget(t *testing.T) {
	for _, charset := range []string{testCharsetUTF8MB4, localFilenameCharset} {
		value := strings.Repeat("测", 130)
		encoded := value
		if charset == localFilenameCharset {
			encoded = strings.Repeat("\xb2\xe2", 130)
		}
		packet, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{
			testDriverDialectAttribute: testDriverDialectValue, testProxyUserAttribute: encoded, testClientAttributeCapability: "3", testProxyRequestCapability: "1360200",
		}})
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Config{User: testUser, ProxyUser: value, Session: config.SessionOptions{Charset: charset}, Limits: config.Limits{MaxMessageBytes: int64(len(packet) - 34)}}
		if _, err = newConnector(cfg); err != nil {
			t.Fatal("encoded proxy identity budget rejected", err)
		}
		cfg.Limits.MaxMessageBytes--
		if _, err = newConnector(cfg); err == nil {
			t.Fatal("proxy identity length prefixes exceeded budget")
		}
	}
}

func TestMultiStatementsCapability(t *testing.T) {
	for _, allow := range []bool{false, true} {
		cfg := config.Config{AllowMultiStatements: allow}
		for _, offer := range []uint64{0, wire.ClientMultiStatements} {
			flags, err := ClientCapabilities(cfg, wire.ClientProtocol41|wire.ClientSecureConnection|offer)
			if err != nil || (flags&wire.ClientMultiStatements != 0) != allow {
				t.Fatalf("allow=%v offer=%x flags=%x err=%v", allow, offer, flags, err)
			}
		}
	}
}

func TestAuthNativeRSARejection(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []string{authentication.SHA256, authentication.CachingSHA2} {
		for _, path := range []string{"no-pinned-key", authRSAUnsolicitedKey, authRSAOversizedPassword} {
			t.Run(plugin+"/"+path, func(t *testing.T) {
				cfg := config.Config{User: testUser, Password: testPassword, Auth: config.AuthOptions{RSAKey: &key.PublicKey}}
				if path == "no-pinned-key" {
					cfg.Auth.RSAKey = nil
				}
				if path == authRSAOversizedPassword {
					cfg.Password = strings.Repeat("x", 256)
				}
				result, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, authGreeting(plugin, false)) {
						return
					}
					if plugin == authentication.CachingSHA2 || path == authRSAUnsolicitedKey {
						if !readAuthResponse(ctx, t, p) {
							return
						}
						packet := []byte{1, 4}
						if path == authRSAUnsolicitedKey {
							packet = append([]byte{1}, []byte("-----BEGIN PUBLIC KEY-----\nserver-supplied-key")...)
						}
						if !sendPackets(ctx, t, p, packet) {
							return
						}
					}
					if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
						t.Error("rejected RSA path wrote an auth response or key retrieval request")
					}
				})
				want := dberrors.ErrTLSRequired
				switch path {
				case authRSAUnsolicitedKey:
					want = wire.ErrMalformed
				case authRSAOversizedPassword:
					want = rsa.ErrMessageTooLong
				}
				if result != nil || !errors.Is(err, want) {
					t.Fatal("RSA rejection lost cause", err)
				}
			})
		}
	}
}

const testProxyCapability = "ob_capability_flag"

const testStandard = "standard"

const testOB20 = "ob20"

const testProxyUser = "client_user"

const testProxyUserAttribute = "__ob_client_proxy_user_name"

const testProxyDetectedMySQL = "detected-mysql"

const dsnTrue = "true"

const dsnFalse = "false"

const authRSAOversizedPassword = "oversized-password"

const authRSAUnsolicitedKey = "unsolicited-key"
