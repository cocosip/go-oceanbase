package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testAttributeKey = "application"
const testAttributeValue = "billing,worker:1"
const testAttributeGB18030 = "gb18030"
const testAttributeEmpty = "empty"
const testAttributePrivate = "private"
const testClientAttributeCapability = "__ob_client_attribute_capability_flag"
const testAttributeGBKValue = "\xb2\xe2\xca\xd4"

const testDriverDialectAttribute = "__mysql_client_type"
const testDriverDialectValue = "__ob_jdbc_client"

func TestConnectionAttributesDriverDialect(t *testing.T) {
	c := &connector{}
	attrs := c.protocolAttributes()
	if attrs[testDriverDialectAttribute] != testDriverDialectValue {
		t.Fatal("OceanBase driver dialect missing from authentication attributes")
	}
	if attrs[testProxyRequestCapability] != "1360200" {
		t.Fatal("driver dialect omitted LOCAL INFILE protocol capability")
	}
}

func TestConnectionAttributesSnapshot(t *testing.T) {
	attributes := map[string]string{testAttributeKey: testAttributeValue, "应用": "测试", testAttributeEmpty: ""}
	created, err := NewConnector(Config{User: testUser, Session: SessionOptions{Charset: localFilenameCharset}, ConnectionAttributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	attributes[testAttributeKey] = "changed"
	delete(attributes, "应用")
	c := created.(*connector)
	for range 2 {
		got, err := c.connectionAttributes("")
		if err != nil || got[testAttributeKey] != testAttributeValue || got["\xd3\xa6\xd3\xc3"] != testAttributeGBKValue || got[testClientAttributeCapability] != "3" {
			t.Fatalf("attributes not encoded from immutable snapshot: %v", err)
		}
		got[testAttributeKey] = "changed"
	}
	if c.cfg.ConnectionAttributes["应用"] != "测试" {
		t.Fatal("snapshot no longer contains Go UTF-8 strings")
	}
}

func TestConnectionAttributesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, charset string
		attributes    map[string]string
		maxBytes      int64
	}{
		{name: "empty-key", attributes: map[string]string{"": testAttributePrivate}},
		{name: "reserved", attributes: map[string]string{testClientAttributeCapability: testAttributePrivate}},
		{name: "proxy-identity", attributes: map[string]string{"__ob_client_proxy_user_name": testAttributePrivate}},
		{name: "future-reserved", attributes: map[string]string{"_future": testAttributePrivate}},
		{name: "server-capability", attributes: map[string]string{"ob_capability_flag": testAttributePrivate}},
		{name: "nul-key", attributes: map[string]string{"private\x00": testAttributeValue}},
		{name: "nul-value", attributes: map[string]string{testAttributeKey: "private\x00"}},
		{name: "invalid-key", attributes: map[string]string{"private\xff": testAttributeValue}},
		{name: "invalid-value", attributes: map[string]string{testAttributeKey: "private\xff"}},
		{name: "unrepresentable-key", charset: "ascii", attributes: map[string]string{"应用": testAttributePrivate}},
		{name: "unrepresentable-value", charset: localFilenameCharset, attributes: map[string]string{testAttributeKey: "😀private"}},
		{name: "byte-budget", attributes: map[string]string{testAttributeKey: strings.Repeat("p", 129)}, maxBytes: 128},
		{name: "combined-budget", attributes: map[string]string{testAttributeKey: strings.Repeat("p", 90)}, maxBytes: 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConnector(Config{User: testUser, Session: SessionOptions{Charset: tc.charset}, ConnectionAttributes: tc.attributes, Limits: Limits{MaxMessageBytes: tc.maxBytes}})
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "ConnectionAttributes" || strings.Contains(err.Error(), testAttributePrivate) {
				t.Fatalf("unsafe attribute validation: %v", err)
			}
		})
	}
}

func TestConnectionAttributesEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		charset, value, encoded string
	}{
		{localFilenameCharset, strings.Repeat("测", 150), strings.Repeat("\xb2\xe2", 150)},
		{testAttributeGB18030, strings.Repeat("\u0080", 100), strings.Repeat("\x81\x30\x81\x30", 100)},
		{charsetUTF8MB4, strings.Repeat("a", 250), strings.Repeat("a", 250)},
		{charsetUTF8MB4, strings.Repeat("a", 251), strings.Repeat("a", 251)},
		{charsetUTF8MB4, strings.Repeat("a", 65535), strings.Repeat("a", 65535)},
		{charsetUTF8MB4, strings.Repeat("a", 65536), strings.Repeat("a", 65536)},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.charset, len(tc.value)), func(t *testing.T) {
			packet, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: tc.encoded, testClientAttributeCapability: "3", testProxyRequestCapability: "1360200"}})
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(len(packet) - 34)
			cfg := Config{User: testUser, Session: SessionOptions{Charset: tc.charset}, ConnectionAttributes: map[string]string{testAttributeKey: tc.value}, Limits: Limits{MaxMessageBytes: limit}}
			created, err := NewConnector(cfg)
			if err != nil {
				t.Fatal("exact encoded attribute budget rejected", err)
			}
			attrs, err := created.(*connector).connectionAttributes("")
			if err != nil || attrs[testAttributeKey] != tc.encoded {
				t.Fatal("encoded attribute bytes differ", err)
			}
			cfg.Limits.MaxMessageBytes--
			if _, err = NewConnector(cfg); err == nil {
				t.Fatal("length prefixes or encoded attribute bytes ignored")
			}
		})
	}
}

func TestConnectionAttributesHandshake(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, interactive := range []bool{false, true} {
			for _, tc := range []struct {
				charset, key, value string
				collation           byte
			}{
				{charsetUTF8MB4, "应用", "测试", 45},
				{localFilenameCharset, "\xd3\xa6\xd3\xc3", testAttributeGBKValue, 28},
			} {
				t.Run(fmt.Sprintf("%d/%t/%s", mode, interactive, tc.charset), func(t *testing.T) {
					cfg := Config{User: testUser, Mode: mode, Interactive: interactive, Session: SessionOptions{Charset: tc.charset}, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue, "应用": "测试", testAttributeEmpty: ""}, Protocol: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff, ProxyChecksum: FeatureOff, NewMetadata: FeatureOff}}
					s, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
						if !sendPackets(ctx, t, p, protocolGreeting(false)) {
							return
						}
						caps := uint64(0x28ba8203)
						if interactive {
							caps |= 1 << 10
						}
						want, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: caps, MaxPacketBytes: 64 << 20, Charset: tc.collation, User: testUser, Plugin: authNative, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: testAttributeValue, tc.key: tc.value, testAttributeEmpty: "", testClientAttributeCapability: "1", testProxyRequestCapability: "1048576"}})
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
						if mode == ModeOracle {
							status = 6
						}
						sendPackets(ctx, t, p, commandPacket(0, 0, status))
					})
					if err != nil || s.mode != mode {
						t.Fatal("attribute handshake failed", err)
					}
				})
			}
		}
	}
}

func TestConnectionAttributesMissingCapability(t *testing.T) {
	calls := 0
	_, err := runAuthTest(t, Config{Credentials: func(context.Context) (Credentials, error) { calls++; return Credentials{User: testUser}, nil }, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue}}, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
			return
		}
		if got, err := p.ReadMessage(ctx); err == nil || len(got) != 0 {
			t.Error("unsupported explicit attributes sent authentication bytes")
		}
	})
	if !errors.Is(err, ErrUnsupported) || calls != 0 {
		t.Fatal("explicit attributes silently dropped or invoked credentials", err, calls)
	}
}

func TestInteractiveCapability(t *testing.T) {
	for _, offered := range []uint64{0, 1 << 10} {
		for _, interactive := range []bool{false, true} {
			c := &connector{cfg: Config{Interactive: interactive}}
			caps, err := c.clientCapabilities(wire.ClientProtocol41 | wire.ClientSecureConnection | offered)
			if err != nil || (caps&(1<<10) != 0) != interactive {
				t.Fatal("interactive client request depends on server offer", caps, err)
			}
		}
	}
	for _, value := range []string{dsnTrue, dsnFalse} {
		cfg, err := ParseDSN("oceanbase://app@localhost/?interactive=" + value)
		if err != nil || cfg.Interactive != (value == dsnTrue) {
			t.Fatal("interactive DSN value ignored", err)
		}
	}
}

func TestConnectionAttributesChangeUser(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, variant := range []struct{ framing, charset string }{{testStandard, charsetUTF8MB4}, {testCompression, charsetUTF8MB4}, {testOB20, charsetUTF8MB4}, {testStandard, localFilenameCharset}, {testCompression, localFilenameCharset}, {testOB20, localFilenameCharset}} {
			framing := variant.framing
			t.Run(fmt.Sprintf("%d/%s/%s", mode, framing, variant.charset), func(t *testing.T) {
				cfg := Config{User: testUser, Mode: mode, Interactive: true, Session: SessionOptions{Charset: variant.charset}, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue, "应用": "测试"}, Protocol: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff}}
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = FeatureRequired
				}
				cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, protocolGreeting(framing == testCompression)) {
						return
					}
					initial, err := p.ReadMessage(ctx)
					if err != nil || len(initial) < 32 {
						t.Error("missing initial response", err)
						return
					}
					caps := uint64(binary.LittleEndian.Uint32(initial))
					if caps&(1<<10) == 0 || caps&wire.ClientConnectAttrs == 0 {
						t.Error("missing requested capabilities")
						return
					}
					status := byte(2)
					if mode == ModeOracle {
						status = 6
					}
					var variables map[string]string
					if framing == testOB20 {
						variables = map[string]string{testProxyCapability: "256"}
					}
					if !sendPackets(ctx, t, p, protocolOK(status, variables)) {
						return
					}
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
					if !serveAttributeBaseline(ctx, t, p, status, variant.charset) {
						return
					}
					ctx = context.Background()
					database := ""
					if mode == ModeOracle {
						database = "next"
					}
					want := changeUserPacket(t, mode, database)
					key, value := "应用", "测试"
					if variant.charset == localFilenameCharset {
						key, value = "\xd3\xa6\xd3\xc3", testAttributeGBKValue
					}
					attrs, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: testAttributeValue, key: value, testClientAttributeCapability: "3", testProxyRequestCapability: map[bool]string{false: "1048648", true: "1360200"}[framing == testOB20]}})
					if err != nil {
						t.Error(err)
						return
					}
					want = append(want, attrs[34:]...)
					if !serveOK(ctx, t, p, want, status) || !serveAttributeBaseline(ctx, t, p, status, variant.charset) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
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
				if err = ChangeUser(ctx, pinned, Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
					t.Fatal(err)
				}
				if err = pinned.PingContext(ctx); err != nil {
					t.Fatal("unread ChangeUser tail", err)
				}
			})
		}
	}
}

func serveAttributeBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, charset string) bool {
	t.Helper()
	return serveOK(ctx, t, p, []byte("\x03SET NAMES "+charset), status) && serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) && serveVersion(ctx, t, p, status, false) && serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}
