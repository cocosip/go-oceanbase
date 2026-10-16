package oceanbase

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"
)

const testUser = "app"
const testPassword = "secret"
const testServerName = "db.example"
const testCustomPlugin = "custom"

func TestAuthConfigSnapshot(t *testing.T) {
	calls := 0
	plugins := map[string]AuthFactory{testCustomPlugin: func() AuthPlugin { return &testAuthPlugin{calls: &calls} }}
	key := &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 2048), E: 65537}
	c, err := NewConnector(Config{Credentials: func(context.Context) (Credentials, error) { calls++; return Credentials{User: testUser}, nil }, Auth: AuthOptions{RSAKey: key, Plugins: plugins}})
	if err != nil {
		t.Fatal(err)
	}
	delete(plugins, testCustomPlugin)
	key.N.SetInt64(1)
	key.E = 3
	snapshot := c.(*connector).cfg
	if snapshot.Auth.Plugins[testCustomPlugin] == nil || snapshot.Auth.RSAKey.N.BitLen() != 2049 || snapshot.Auth.RSAKey.E != 65537 || calls != 0 {
		t.Fatal("auth configuration was not snapshotted without callbacks")
	}
	for _, options := range []AuthOptions{{Plugins: map[string]AuthFactory{"bad\x00name": func() AuthPlugin { return nil }}}, {Plugins: map[string]AuthFactory{testCustomPlugin: nil}}, {RSAKey: &rsa.PublicKey{N: big.NewInt(1), E: 2}}} {
		if _, err := NewConnector(Config{User: testUser, Auth: options}); err == nil {
			t.Fatal("invalid auth configuration accepted")
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	c, err := NewConnector(Config{User: testUser})
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.(*connector).cfg
	if cfg.Addr != "127.0.0.1:2881" || cfg.Mode != ModeAuto || cfg.ConnectTimeout != 10*time.Second {
		t.Fatalf("unexpected connection defaults: addr=%q mode=%v timeout=%v", cfg.Addr, cfg.Mode, cfg.ConnectTimeout)
	}
	if cfg.Limits.MaxMessageBytes != 64<<20 || cfg.Limits.MaxLOBBytes != 16<<20 || cfg.Limits.FetchSize != 128 || cfg.Limits.MaxResultItems != 1024 || cfg.Limits.CleanupTimeout != time.Second {
		t.Fatalf("unexpected resource defaults: %+v", cfg.Limits)
	}
	if cfg.RowsAffectedMode != MatchedRows || cfg.ResetMode != ResetManaged || cfg.AllowMultiStatements || cfg.Logger != nil || cfg.TLSConfig != nil {
		t.Fatal("unsafe defaults")
	}
	if cfg.Session.Charset != "utf8mb4" || cfg.Session.Location != time.UTC {
		t.Fatal("unstable charset/time defaults")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"missing user", Config{}},
		{"mode", Config{User: testUser, Mode: 99}},
		{"negative timeout", Config{User: testUser, ConnectTimeout: -1}},
		{"negative message limit", Config{User: testUser, Limits: Limits{MaxMessageBytes: -1}}},
		{"negative lob limit", Config{User: testUser, Limits: Limits{MaxLOBBytes: -1}}},
		{"negative fetch", Config{User: testUser, Limits: Limits{FetchSize: -1}}},
		{"negative result limit", Config{User: testUser, Limits: Limits{MaxResultItems: -1}}},
		{"negative cleanup", Config{User: testUser, Limits: Limits{CleanupTimeout: -1}}},
		{"insecure TLS", Config{User: testUser, TLSConfig: &tls.Config{InsecureSkipVerify: true}}},
		{"invalid counts", Config{User: testUser, RowsAffectedMode: 99}},
		{"oracle changed counts", Config{User: testUser, Mode: ModeOracle, RowsAffectedMode: ChangedRows}},
		{"invalid reset", Config{User: testUser, ResetMode: 99}},
		{"invalid address", Config{User: testUser, Addr: "host:0"}},
		{"invalid port", Config{User: testUser, Addr: "host:65536"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConnector(tc.cfg)
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("want ConfigError, got %v", err)
			}
		})
	}
}

func TestConnectorSnapshot(t *testing.T) {
	const mutated = "changed"
	variables := map[string]string{"time_zone": "+00:00"}
	initSQL := []string{"SET @a=1"}
	protos := []string{"original"}
	roots := x509.NewCertPool()
	certDER := []byte{1, 2, 3}
	tlsConfig := &tls.Config{ServerName: testServerName, NextProtos: protos, RootCAs: roots, Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}}}}
	calls := 0
	c, err := NewConnector(Config{User: testUser, Session: SessionOptions{Variables: variables, InitSQL: initSQL}, TLSConfig: tlsConfig, DialContext: func(context.Context, string, string) (net.Conn, error) {
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
	snapshot := c.(*connector).cfg
	if snapshot.Session.Variables["time_zone"] != "+00:00" || snapshot.Session.InitSQL[0] != "SET @a=1" || snapshot.TLSConfig.ServerName != testServerName || snapshot.TLSConfig.NextProtos[0] != "original" || snapshot.TLSConfig.Certificates[0].Certificate[0][0] != 1 || snapshot.TLSConfig.RootCAs == roots {
		t.Fatal("connector retained mutable caller configuration")
	}
	if calls != 0 {
		t.Fatal("NewConnector performed network I/O")
	}
}

func TestDriverRegistration(t *testing.T) {
	db, err := sql.Open("oceanbase", "oceanbase://app:secret@localhost/demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = db.PingContext(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("D01 must explicitly reject unavailable Connect: %v", err)
	}
	if _, err = sql.Open("oceanbase", "oceanbase://app:secret@localhost/demo?unknown=secret"); err == nil {
		t.Fatal("DriverContext must validate DSN before dialing")
	}
}
