package connect

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAuthConfigSnapshot(t *testing.T) {
	calls := 0
	plugins := map[string]config.AuthFactory{testCustomPlugin: func() config.AuthPlugin { return &testAuthPlugin{calls: &calls} }}
	key := &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 2048), E: 65537}
	c, err := newConnector(config.Config{Credentials: func(context.Context) (config.Credentials, error) {
		calls++
		return config.Credentials{User: testUser}, nil
	}, Auth: config.AuthOptions{RSAKey: key, Plugins: plugins}})
	if err != nil {
		t.Fatal(err)
	}
	delete(plugins, testCustomPlugin)
	key.N.SetInt64(1)
	key.E = 3
	snapshot := c.cfg
	if snapshot.Auth.Plugins[testCustomPlugin] == nil || snapshot.Auth.RSAKey.N.BitLen() != 2049 || snapshot.Auth.RSAKey.E != 65537 || calls != 0 {
		t.Fatal("auth configuration was not snapshotted without callbacks")
	}
	for _, options := range []config.AuthOptions{{Plugins: map[string]config.AuthFactory{"bad\x00name": func() config.AuthPlugin { return nil }}}, {Plugins: map[string]config.AuthFactory{testCustomPlugin: nil}}, {RSAKey: &rsa.PublicKey{N: big.NewInt(1), E: 2}}} {
		if _, err := newConnector(config.Config{User: testUser, Auth: options}); err == nil {
			t.Fatal("invalid auth configuration accepted")
		}
	}
}

func TestConnectorSnapshot(t *testing.T) {
	const mutated = "changed"
	variables := map[string]string{"time_zone": testUTCOffset}
	initSQL := []string{"SET @a=1"}
	protos := []string{"original"}
	roots := x509.NewCertPool()
	certDER := []byte{1, 2, 3}
	tlsConfig := &tls.Config{ServerName: testServerName, NextProtos: protos, RootCAs: roots, Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}}}}
	calls := 0
	c, err := newConnector(config.Config{User: testUser, Session: config.SessionOptions{Variables: variables, InitSQL: initSQL}, TLSConfig: tlsConfig, DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("unexpected dial")
	}})
	if err != nil {
		t.Fatal(err)
	}
	variables["time_zone"] = mutated
	initSQL[0] = mutated
	protos[0] = mutated
	tlsConfig.ServerName = mutated
	certDER[0] = 9
	snapshot := c.cfg
	if snapshot.Session.Variables["time_zone"] != testUTCOffset || snapshot.Session.InitSQL[0] != "SET @a=1" || snapshot.TLSConfig.ServerName != testServerName || snapshot.TLSConfig.NextProtos[0] != "original" || snapshot.TLSConfig.Certificates[0].Certificate[0][0] != 1 || snapshot.TLSConfig.RootCAs == roots {
		t.Fatal("connector retained mutable caller configuration")
	}
	if calls != 0 {
		t.Fatal("NewConnector performed network I/O")
	}
}

func TestConnectionAttributesDriverDialect(t *testing.T) {
	attrs := ProtocolAttributes(config.Config{})
	if attrs[testDriverDialectAttribute] != testDriverDialectValue {
		t.Fatal("OceanBase driver dialect missing from authentication attributes")
	}
	if attrs[testProxyRequestCapability] != "1360200" {
		t.Fatal("driver dialect omitted LOCAL INFILE protocol capability")
	}
}

func TestConnectionAttributesSnapshot(t *testing.T) {
	attributes := map[string]string{testAttributeKey: testAttributeValue, "应用": "测试", testAttributeEmpty: ""}
	created, err := newConnector(config.Config{User: testUser, Session: config.SessionOptions{Charset: localFilenameCharset}, ConnectionAttributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	attributes[testAttributeKey] = "changed"
	delete(attributes, "应用")
	c := created
	for range 2 {
		got, err := ConnectionAttributes(c.cfg, "")
		if err != nil || got[testAttributeKey] != testAttributeValue || got["\xd3\xa6\xd3\xc3"] != testAttributeGBKValue || got[testClientAttributeCapability] != "7" {
			t.Fatalf("attributes not encoded from immutable snapshot: %v", err)
		}
		got[testAttributeKey] = "changed"
	}
	if c.cfg.ConnectionAttributes["应用"] != "测试" {
		t.Fatal("snapshot no longer contains Go UTF-8 strings")
	}
}

func TestConnectionAttributesEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		charset, value, encoded string
	}{
		{localFilenameCharset, strings.Repeat("测", 150), strings.Repeat("\xb2\xe2", 150)},
		{testAttributeGB18030, strings.Repeat("\u0080", 100), strings.Repeat("\x81\x30\x81\x30", 100)},
		{testCharsetUTF8MB4, strings.Repeat("a", 250), strings.Repeat("a", 250)},
		{testCharsetUTF8MB4, strings.Repeat("a", 251), strings.Repeat("a", 251)},
		{testCharsetUTF8MB4, strings.Repeat("a", 65535), strings.Repeat("a", 65535)},
		{testCharsetUTF8MB4, strings.Repeat("a", 65536), strings.Repeat("a", 65536)},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.charset, len(tc.value)), func(t *testing.T) {
			packet, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: tc.encoded, testClientAttributeCapability: "7", testProxyRequestCapability: "1360200"}})
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(len(packet) - 34)
			cfg := config.Config{User: testUser, Session: config.SessionOptions{Charset: tc.charset}, ConnectionAttributes: map[string]string{testAttributeKey: tc.value}, Limits: config.Limits{MaxMessageBytes: limit}}
			created, err := newConnector(cfg)
			if err != nil {
				t.Fatal("exact encoded attribute budget rejected", err)
			}
			attrs, err := ConnectionAttributes(created.cfg, "")
			if err != nil || attrs[testAttributeKey] != tc.encoded {
				t.Fatal("encoded attribute bytes differ", err)
			}
			cfg.Limits.MaxMessageBytes--
			if _, err = newConnector(cfg); err == nil {
				t.Fatal("length prefixes or encoded attribute bytes ignored")
			}
		})
	}
}

func TestRoutingSnapshot(t *testing.T) {
	endpoints := []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}
	created, err := newConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Endpoints: endpoints}})
	if err != nil {
		t.Fatal(err)
	}
	endpoints[0].Addr = "mutated"
	c := created
	got, _ := c.routes.Order(time.Now())
	if got[0].Addr != routeFirst || c.cfg.Routing.Endpoints[0].Addr != routeFirst {
		t.Fatal("mutable routing configuration retained")
	}
	for _, o := range []config.RoutingOptions{{Role: 99}, {Strategy: 99}, {Endpoints: []config.Endpoint{{Addr: "a"}, {Addr: "A:2881"}}}, {Endpoints: []config.Endpoint{{}}}, {Role: config.RoleReadOnly, Endpoints: []config.Endpoint{{Addr: routeFirst}}}, {MaxAttempts: 4097}, {ValidationInterval: -1}} {
		if _, err = newConnector(config.Config{User: testUser, Routing: o}); err == nil {
			t.Fatal(o)
		}
	}
}

func TestConnectSessionValueEncoding(t *testing.T) {
	for _, tc := range []struct {
		mode        config.Mode
		status      uint16
		value, want string
	}{
		{config.ModeMySQL, 2, escapedValue, "'x\\\\''y'"},
		{config.ModeMySQL, 514, escapedValue, testSessionQuotedNoBackslash},
		{config.ModeOracle, 6, escapedValue, testSessionQuotedNoBackslash},
	} {
		physical := session.New(config.Config{}, &session.Negotiated{Mode: tc.mode, OK: wire.OK{Status: tc.status}})
		op, err := physical.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		value := (baseline{op: op}).quote(tc.value)
		op.Release()
		if value != tc.want {
			t.Fatalf("unsafe SET literal: %q want %q", value, tc.want)
		}
	}
}

func newConnector(cfg config.Config) (*Connector, error) {
	normalized, err := config.Normalize(cfg)
	if err != nil {
		return nil, err
	}
	return New(normalized)
}

const testUTCOffset = "+00:00"

const testAttributeEmpty = "empty"

const testDriverDialectAttribute = "__mysql_client_type"

const testDriverDialectValue = "__ob_jdbc_client"

const testProxyRequestCapability = "__proxy_capability_flag"

const routeFirst = "first:2881"

const routeSecond = "second:2881"

const escapedValue = "x\\'y"
