package oceanbase

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const platformTestSocket = "/socket"

func runPlatformPeer(t *testing.T, listener net.Listener, cfg Config) {
	t.Helper()
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = server.Close() }()
		if err = server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
		ctx := context.Background()
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("platform peer did not close")
	}
}

func TestPlatformTransportUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ob.sock")
	listener, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformPeer(t, listener, Config{Network: networkUnix, Addr: path, User: testUser})
}

func runPlatformTLSGate(t *testing.T, listener net.Listener, cfg Config) {
	t.Helper()
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = raw.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p := wire.NewPacketIO(raw, 64<<20)
		if err = p.WriteMessage(ctx, authGreeting(authClear, false)); err != nil {
			t.Error(err)
			return
		}
		if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
			t.Error("IPC sent plaintext credentials")
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, ErrTLSRequired) {
		t.Fatal("IPC got plaintext exemption", err)
	}
}

func TestPlatformTransportUnixTLSGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tls.sock")
	listener, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformTLSGate(t, listener, Config{User: testUser, Password: testPassword, Network: networkUnix, Addr: path})
}

func TestPlatformTransportUnixDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeout.sock")
	listener, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	created, err := NewConnector(Config{User: testUser, Network: networkUnix, Addr: path, ConnectTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	go func() { _, e := created.Connect(context.Background()); clientDone <- e }()
	raw, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if err = <-clientDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Unix handshake budget", err)
	}
	if err = raw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = raw.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatal("expired handshake retained transport", err)
	}
}

func TestPlatformTransportConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Network: "udp", Addr: "localhost:2881"},
		{Network: networkUnix}, {Network: networkUnix, Addr: "bad\x00path"},
		{Network: networkUnix, Addr: platformTestSocket, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: "localhost:2881"}}}},
		{Network: networkUnix, Addr: platformTestSocket, Discovery: DiscoveryOptions{Service: "service", Source: func(context.Context) (Topology, error) { return Topology{}, nil }}},
		{Network: networkUnix, Addr: platformTestSocket, TLSConfig: withoutServerName(t)},
		{Network: networkPipe, Addr: "ordinary-file"}, {Network: networkPipe, Addr: `\\.\pipe\`},
		{Network: networkPipe, Addr: `\\?\pipe\oceanbase`},
		{Network: networkPipe, Addr: `\\.\pipe\..\ordinary-file`},
	} {
		cfg.User = testUser
		if _, err := NewConnector(cfg); err == nil {
			t.Fatalf("accepted invalid network %s", cfg.Network)
		}
	}
	var seen bool
	created, err := NewConnector(Config{User: testUser, Network: networkUnix, Addr: platformTestSocket, DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
		seen = network == networkUnix && addr == platformTestSocket
		return nil, context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, context.Canceled) || !seen {
		t.Fatal("custom network mapping", err)
	}
}

func TestPlatformTransportDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://user@localhost/?network=unix&socket=%2Frun%2Fob.sock")
	if err != nil || cfg.Network != networkUnix || cfg.Addr != "/run/ob.sock" {
		t.Fatalf("socket DSN: %s %s %v", cfg.Network, cfg.Addr, err)
	}
	if _, err = NewConnector(cfg); err != nil {
		t.Fatal("normalized IPC config cannot create Connector", err)
	}
	for _, query := range []string{"socket=%2Frun%2Fob.sock", "network=unix", "network=unix&network=pipe&socket=x", "network=tcp&socket=x", "network=pipe&socket=regular-file"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?" + query); err == nil {
			t.Fatalf("invalid socket query %s", query)
		}
	}
}

func withoutServerName(t *testing.T) *tls.Config {
	t.Helper()
	cfg, _ := testTLSConfig(t)
	cfg.ServerName = ""
	return cfg
}
