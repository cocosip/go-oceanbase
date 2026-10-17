package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testProxyUser = "client_user"
const testProxyUserAttribute = "__ob_client_proxy_user_name"
const testProxyDetectedMySQL = "detected-mysql"
const testProxyRequestCapability = "__proxy_capability_flag"

func TestProxyUserConfig(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@localhost/?mode=oracle&proxyUser=client_user")
	if err != nil || cfg.ProxyUser != testProxyUser {
		t.Fatal("explicit proxy identity not parsed", err)
	}
	for _, tc := range []struct {
		value, charset string
		mode           Mode
	}{
		{"proxy-private\x00", "", ModeOracle},
		{"proxy-private\xff", "", ModeOracle},
		{"private测试", "ascii", ModeOracle},
		{testProxyUser, "", ModeMySQL},
	} {
		_, err := NewConnector(Config{User: testUser, ProxyUser: tc.value, Mode: tc.mode, Session: SessionOptions{Charset: tc.charset}})
		var invalid *ConfigError
		if !errors.As(err, &invalid) || invalid.Field != "ProxyUser" || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe proxy identity accepted or exposed", err)
		}
	}
	for _, option := range []string{"proxyUser=", "proxyUser=a&proxyUser=b"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option); err == nil {
			t.Fatal("invalid proxyUser option accepted")
		}
	}
}

func TestProxyUserHandshake(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		cfg := Config{User: testUser, ProxyUser: testProxyUser, Mode: ModeOracle,
			Protocol: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff, NewMetadata: FeatureOff, ProxyChecksum: FeatureOff}}
		if dynamic {
			cfg.ProxyUser = "overridden"
			cfg.Credentials = func(context.Context) (Credentials, error) {
				return Credentials{User: testUser, ProxyUser: testProxyUser}, nil
			}
		}
		s, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
			if !sendPackets(ctx, t, p, protocolGreeting(false)) {
				return
			}
			want, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{
				Capabilities: 0x28ba8203, MaxPacketBytes: 64 << 20, Charset: 45, User: testUser, Plugin: authNative,
				Attributes: map[string]string{testClientAttributeCapability: "1", testProxyUserAttribute: testProxyUser},
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
		if err != nil || s.user != testUser || s.proxyUser != testProxyUser {
			t.Fatal("proxy authentication identity not preserved", err)
		}
	}
}

func TestProxyUserRejection(t *testing.T) {
	for _, path := range []string{"missing-capability", "dynamic-capability", "dynamic-encoding", "dynamic-mysql", testProxyDetectedMySQL, serverErrorPath} {
		t.Run(path, func(t *testing.T) {
			cfg := Config{User: testUser, ProxyUser: testProxyUser}
			calls := 0
			if strings.HasPrefix(path, "dynamic-") {
				cfg.ProxyUser = ""
				cfg.Credentials = func(context.Context) (Credentials, error) {
					calls++
					proxy := testProxyUser
					if path == "dynamic-encoding" {
						proxy = "bad\xff"
					}
					return Credentials{User: testUser, ProxyUser: proxy}, nil
				}
			}
			if path == "dynamic-mysql" {
				cfg.Mode = ModeMySQL
			}
			_, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
				greeting := protocolGreeting(false)
				if path == "missing-capability" || path == "dynamic-capability" {
					greeting = authGreeting(authNative, false)
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
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal("capability or mode failure hidden", err)
				}
			}
			if path == serverErrorPath {
				var server *Error
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
	s, err := runAuthTest(t, Config{User: testUser, ProxyUser: testProxyUser, Credentials: func(context.Context) (Credentials, error) {
		return Credentials{User: testUser}, nil
	}}, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || bytes.Contains(response, []byte(testProxyUserAttribute)) {
			t.Error("cleared proxy target was still sent", err)
			return
		}
		sendPackets(ctx, t, p, commandPacket(0, 0, 2))
	})
	if err != nil || s.proxyUser != "" || s.mode != ModeMySQL {
		t.Fatal("credential callback could not clear static proxy target", err)
	}
}

func TestProxyUserVerifiedTLS(t *testing.T) {
	clientCfg, serverCfg := testTLSConfig(t)
	s, err := runAuthConnTest(t, Config{User: testUser, ProxyUser: testProxyUser, Mode: ModeOracle, TLSConfig: clientCfg}, nil,
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
			if err := p.ReplaceConn(authPeerConn{secure}); err != nil {
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
	if err != nil || !s.tlsVerified || s.proxyUser != testProxyUser {
		t.Fatal("verified TLS proxy authentication rejected", err)
	}
}

func TestProxyUserChangeUserPreflight(t *testing.T) {
	for _, attributes := range []bool{false, true} {
		t.Run(fmt.Sprint(attributes), func(t *testing.T) {
			created, err := NewConnector(Config{User: testUser, Mode: ModeOracle, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				greeting := authGreeting(authNative, false)
				if attributes {
					greeting = protocolGreeting(false)
				}
				if !sendPackets(ctx, t, p, greeting) || !readAuthResponse(ctx, t, p) || !sendPackets(ctx, t, p, commandPacket(0, 0, 6)) || !serveBaseline(ctx, t, p, 6) {
					return
				}
				ctx = context.Background()
				if !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			proxy := testProxyUser
			if attributes {
				proxy = "invalid\x00proxy"
			}
			err = ChangeUser(ctx, pinned, Credentials{User: changeUserName, ProxyUser: proxy}, "")
			if err == nil || !attributes && !errors.Is(err, ErrUnsupported) {
				t.Fatal("proxy preflight failure not rejected", err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal("proxy preflight changed original identity", err)
			}
		})
	}
}

func TestProxyUserEncodedBudget(t *testing.T) {
	for _, charset := range []string{charsetUTF8MB4, localFilenameCharset} {
		value := strings.Repeat("测", 130)
		encoded := value
		if charset == localFilenameCharset {
			encoded = strings.Repeat("\xb2\xe2", 130)
		}
		packet, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{
			testProxyUserAttribute: encoded, testClientAttributeCapability: "3", testProxyRequestCapability: "311624",
		}})
		if err != nil {
			t.Fatal(err)
		}
		cfg := Config{User: testUser, ProxyUser: value, Session: SessionOptions{Charset: charset}, Limits: Limits{MaxMessageBytes: int64(len(packet) - 34)}}
		if _, err = NewConnector(cfg); err != nil {
			t.Fatal("encoded proxy identity budget rejected", err)
		}
		cfg.Limits.MaxMessageBytes--
		if _, err = NewConnector(cfg); err == nil {
			t.Fatal("proxy identity length prefixes exceeded budget")
		}
	}
}

func TestProxyUserChangeUser(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, charset := range []string{charsetUTF8MB4, localFilenameCharset} {
			t.Run(framing+"/"+charset, func(t *testing.T) { testProxyUserChangeUser(t, framing, charset) })
		}
	}
}

func testProxyUserChangeUser(t *testing.T, framing, charset string) {
	t.Helper()
	cfg := Config{User: testUser, ProxyUser: testProxyUser, Mode: ModeOracle, Session: SessionOptions{Charset: charset},
		Protocol: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff, Checksum: FeatureOff, NewMetadata: FeatureOff, ProxyChecksum: FeatureOff, FullLinkTrace: FeatureOff}}
	if framing == testCompression {
		cfg.Protocol.Compression = FeatureRequired
	}
	if framing == testOB20 {
		cfg.Protocol.OB20 = FeatureRequired
	}
	cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, protocolGreeting(framing == testCompression)) || !readAuthResponse(ctx, t, p) {
			return
		}
		var variables map[string]string
		if framing == testOB20 {
			variables = map[string]string{testProxyCapability: "256"}
		}
		if !sendPackets(ctx, t, p, protocolOK(6, variables)) {
			return
		}
		var err error
		if framing == testCompression {
			err = p.EnableCompression()
		}
		if framing == testOB20 {
			err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
		}
		if err != nil {
			t.Error(err)
			return
		}
		if !serveAttributeBaseline(ctx, t, p, 6, charset) {
			return
		}
		ctx = context.Background()
		for _, proxy := range []string{"测试", ""} {
			attrs := map[string]string{testClientAttributeCapability: "1"}
			if proxy != "" {
				attrs[testProxyUserAttribute] = proxy
				if charset == localFilenameCharset {
					attrs[testProxyUserAttribute] = testAttributeGBKValue
				}
			}
			if framing == testOB20 {
				attrs[testProxyRequestCapability] = "33024"
			}
			encoded, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: attrs})
			if err != nil {
				t.Error(err)
				return
			}
			want := append(changeUserPacket(t, ModeOracle, ""), encoded[34:]...)
			if !serveOK(ctx, t, p, want, 6) || !serveAttributeBaseline(ctx, t, p, 6, charset) || !serveOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, proxy := range []string{"测试", ""} {
		if err = ChangeUser(ctx, pinned, Credentials{User: changeUserName, Password: changeUserPassword, ProxyUser: proxy}, ""); err != nil {
			t.Fatal("explicit proxy identity change rejected", err)
		}
		if err = pinned.Raw(func(raw any) error {
			c := raw.(*conn)
			if c.session.proxyUser != proxy || c.cfg.ProxyUser != proxy || c.cfg.Password != "" {
				return errors.New("changed proxy identity not published or password retained")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal("proxy change left unread response", err)
		}
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("changed proxy identity returned to pool", err)
	}
}

func TestProxyUserCancelIdentity(t *testing.T) {
	for _, auxProxy := range []string{"", "other_client", testProxyUser} {
		t.Run(fmt.Sprintf("aux=%s", auxProxy), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started, finish := make(chan struct{}), make(chan struct{})
			main, err := NewConnector(Config{User: testUser, ProxyUser: testProxyUser, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !serveProxyIdentity(ctx, t, p, 42) || !serveBaseline(ctx, t, p, 6) || !readQuery(ctx, t, p, "SELECT waiting") {
					return
				}
				close(started)
				select {
				case <-finish:
				case <-ctx.Done():
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(0, 0, 6)) || !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(main)
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			target, err := QueryTarget(ctx, pinned)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := pinned.ExecContext(ctx, "SELECT waiting"); done <- err }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			aux, err := NewConnector(Config{User: testUser, Credentials: func(context.Context) (Credentials, error) {
				return Credentials{User: testUser, ProxyUser: auxProxy}, nil
			}, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				defer close(finish)
				if !serveProxyIdentity(ctx, t, p, 43) || !serveBaseline(ctx, t, p, 6) {
					return
				}
				if auxProxy == testProxyUser && !serveOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			err = CancelQuery(ctx, aux, target)
			if auxProxy == testProxyUser && err != nil || auxProxy != testProxyUser && !errors.Is(err, ErrResource) {
				t.Fatal("cancel failed to distinguish effective proxy identities", err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal("identity rejection discarded original session", err)
			}
		})
	}
}

func serveProxyIdentity(ctx context.Context, t *testing.T, p *wire.PacketIO, id uint32) bool {
	t.Helper()
	greeting := protocolGreeting(false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return sendPackets(ctx, t, p, greeting) && readAuthResponse(ctx, t, p) && sendPackets(ctx, t, p, commandPacket(0, 0, 6))
}
