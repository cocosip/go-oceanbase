package oceanbase

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func obProxyTestSpec(t *testing.T) obProxySpec {
	t.Helper()
	spec, err := newOBProxySpec(filepath.Join(t.TempDir(), "proxy-host"), filepath.Join(t.TempDir(), "proxy-library"), "enable_strict_stat_time=false")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestOBProxyConfiguration(t *testing.T) {
	library := filepath.Join(t.TempDir(), "proxy-library")
	for _, tc := range []struct{ library, config string }{
		{"", ""}, {"libobproxy.so", ""}, {library + "\x00", ""}, {library, "x\x00y"},
		{library, string([]byte{0xff})}, {library, strings.Repeat("x", 4096)},
	} {
		if _, err := newOBProxySpec(library, tc.library, tc.config); err == nil {
			t.Fatal("unsafe native input accepted")
		}
	}
	for _, host := range []string{"", "proxy-host", library + "\x00", string([]byte{0xff})} {
		if _, err := newOBProxySpec(host, library, ""); err == nil {
			t.Fatal("unsafe host path accepted")
		}
	}
	for _, config := range []string{"", "x=\"y\"", strings.Repeat("x", 4095)} {
		spec, err := newOBProxySpec(library, library, config)
		if err != nil || !filepath.IsAbs(spec.socket) || !filepath.IsAbs(spec.directory) {
			t.Fatal("valid native input rejected", err)
		}
	}
	if !obProxyEnabled {
		if dial, err := NewOBProxyDialer(library, library, ""); dial != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatal("unsupported platform did not reject", err)
		}
	}
	for _, key := range []string{"obProxySocket", "obProxyConfig"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + key + "=true"); err == nil {
			t.Fatal("Java VC option accepted")
		}
	}
}

func TestOBProxySingleInitialization(t *testing.T) {
	spec := obProxyTestSpec(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	r := &obProxyRuntime{initialize: func(got obProxySpec) error {
		if got != spec {
			t.Error("native runtime identity changed")
		}
		calls.Add(1)
		close(entered)
		<-release
		return nil
	}}
	var once sync.Once
	defer once.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	const count = 12
	result := make(chan error, count)
	for range count {
		go func() { result <- r.start(ctx, spec) }()
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, change := range []func(*obProxySpec){
		func(s *obProxySpec) { s.library += ".other" },
		func(s *obProxySpec) { s.host += ".other" },
		func(s *obProxySpec) { s.config = "{}" },
		func(s *obProxySpec) { s.directory += ".different-directory" },
	} {
		other := spec
		change(&other)
		if err := r.start(ctx, other); err == nil {
			t.Fatal("conflicting process runtime identity accepted")
		}
	}
	once.Do(func() { close(release) })
	for range count {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.start(ctx, spec); err != nil || calls.Load() != 1 {
		t.Fatal("runtime was initialized again", err, calls.Load())
	}
}

func TestOBProxyInitializationCancellation(t *testing.T) {
	spec := obProxyTestSpec(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	r := &obProxyRuntime{initialize: func(obProxySpec) error {
		calls.Add(1)
		close(entered)
		<-release
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.start(ctx, spec); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("already canceled call started native runtime", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wait, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- r.start(wait, spec) }()
	var once sync.Once
	defer once.Do(func() { close(release) })
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("waiting cancellation lost", err)
	}
	once.Do(func() { close(release) })
	if err := r.start(ctx, spec); err != nil || calls.Load() != 1 {
		t.Fatal("canceled waiter caused another native initialization", err)
	}
}

func TestOBProxyInitializationFailure(t *testing.T) {
	spec := obProxyTestSpec(t)
	want := errors.New("native initialization failed")
	var calls atomic.Int32
	r := &obProxyRuntime{initialize: func(obProxySpec) error { calls.Add(1); return want }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 2 {
		if err := r.start(ctx, spec); !errors.Is(err, want) {
			t.Fatal("native error lost", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failed native runtime was restarted")
	}
}

func TestOBProxyDialerTarget(t *testing.T) {
	spec := obProxyTestSpec(t)
	var calls atomic.Int32
	r := &obProxyRuntime{initialize: func(obProxySpec) error { calls.Add(1); return nil }}
	dial := r.dialer(spec)
	for _, target := range [][2]string{{networkTCP, spec.socket}, {networkUnix, spec.socket + ".other"}, {networkUnix, "/arbitrary"}} {
		if c, err := dial(context.Background(), target[0], target[1]); err == nil || c != nil {
			t.Fatal("dialer accepted a different native endpoint")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid endpoint initialized native runtime")
	}
}

func TestOBProxyDirectoryChange(t *testing.T) {
	spec := obProxyTestSpec(t)
	r := &obProxyRuntime{initialize: func(obProxySpec) error { t.Error("native call after directory change"); return nil }}
	t.Chdir(t.TempDir())
	if err := r.start(context.Background(), spec); err == nil {
		t.Fatal("changed working directory accepted")
	}
}

func TestOBProxyUnixPathLimit(t *testing.T) {
	library := filepath.Join(t.TempDir(), "proxy-library")
	directory := filepath.Join(t.TempDir(), strings.Repeat("long", 25))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	if _, err := newOBProxySpec(library, library, ""); err == nil {
		t.Fatal("native sockaddr_un path would be truncated")
	}
}

func TestOBProxyDialerDeadlineAndClose(t *testing.T) {
	spec := obProxyTestSpec(t)
	// A short real Unix endpoint tests Go deadline/close behavior without starting OBProxy.
	spec.socket = filepath.Join(t.TempDir(), "vc.sock")
	l, err := net.Listen(networkUnix, spec.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var ready atomic.Bool
	r := &obProxyRuntime{initialize: func(obProxySpec) error { ready.Store(true); return nil }}
	c, err := r.dialer(spec)(ctx, networkUnix, spec.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	s, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !ready.Load() {
		t.Fatal("Unix dial bypassed embedded initialization")
	}
	if err = c.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("read deadline lost", err)
	}
	if err = c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := c.Read(make([]byte, 1)); done <- e }()
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal("close did not interrupt read", err)
		}
	case <-ctx.Done():
		t.Fatal("close blocked")
	}
}

func obProxyProtocolPeer(t *testing.T, serve func(net.Conn)) obProxySpec {
	t.Helper()
	spec := obProxyTestSpec(t)
	spec.socket = filepath.Join(t.TempDir(), "vc.sock")
	listener, err := net.Listen(networkUnix, spec.socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Error(err)
			}
			return
		}
		defer func() { _ = server.Close() }()
		// PacketIO resets per-message deadlines, including background QUIT reads.
		stop := time.AfterFunc(5*time.Second, func() { _ = server.Close() })
		defer stop.Stop()
		serve(server)
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return spec
}

type obProxyCloseConn struct {
	net.Conn
	closed *atomic.Int32
}

func (c obProxyCloseConn) Close() error {
	err := c.Conn.Close()
	c.closed.Add(1)
	return err
}

func TestOBProxyProtocolSessions(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", status, framing), func(t *testing.T) {
				var starts, closed atomic.Int32
				spec := obProxyProtocolPeer(t, func(server net.Conn) {
					if starts.Load() != 1 {
						t.Error("Unix dial bypassed native initialization")
					}
					p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
					ctx := context.Background()
					if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					_ = readCommand(ctx, t, p, []byte{1})
				})
				r := &obProxyRuntime{initialize: func(obProxySpec) error { starts.Add(1); return nil }}
				dial := r.dialer(spec)
				cfg := Config{User: testUser, Mode: modeFromStatus(status), Network: networkUnix, Addr: spec.socket,
					DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
						c, e := dial(ctx, network, address)
						if e != nil {
							return nil, e
						}
						return obProxyCloseConn{Conn: c, closed: &closed}, nil
					}}
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = FeatureRequired
				}
				created, err := NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(created)
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				for range 2 {
					if err = db.PingContext(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err = db.Close(); err != nil || starts.Load() != 1 || closed.Load() != 1 {
					t.Fatal("pool reuse or transport close failed", err, closed.Load())
				}
			})
		}
	}
}

func TestOBProxyTLSAndAuthentication(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			clientTLS, serverTLS := testTLSConfig(t)
			spec := obProxyProtocolPeer(t, func(server net.Conn) {
				ctx := context.Background()
				p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
				if !sendPackets(ctx, t, p, authGreeting(authClear, true)) || !readAuthResponse(ctx, t, p) {
					return
				}
				secure := tls.Server(server, serverTLS)
				if e := secure.HandshakeContext(ctx); e != nil {
					t.Error(e)
					return
				}
				if e := p.ReplaceConn(authPeerConn{secure}); e != nil {
					t.Error(e)
					return
				}
				if b, e := p.ReadMessage(ctx); e != nil || !strings.Contains(string(b), "vc-secret") {
					t.Errorf("missing TLS credentials: %x %v", b, e)
					return
				}
				if !sendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0}) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
					return
				}
				_ = readCommand(ctx, t, p, []byte{1})
			})
			r := &obProxyRuntime{initialize: func(obProxySpec) error { return nil }}
			created, err := NewConnector(Config{User: testUser, Password: "vc-secret", Mode: modeFromStatus(status), TLSConfig: clientTLS,
				Network: networkUnix, Addr: spec.socket, DialContext: r.dialer(spec)})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err = db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOBProxyPlaintextGate(t *testing.T) {
	spec := obProxyProtocolPeer(t, func(server net.Conn) {
		ctx := context.Background()
		p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
		if !sendPackets(ctx, t, p, authGreeting(authClear, false)) {
			return
		}
		if b, e := p.ReadMessage(ctx); e == nil || len(b) != 0 {
			t.Error("VC exposed cleartext credentials without verified TLS")
		}
	})
	r := &obProxyRuntime{initialize: func(obProxySpec) error { return nil }}
	created, err := NewConnector(Config{Network: networkUnix, Addr: spec.socket, DialContext: r.dialer(spec), User: testUser, Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if c, e := created.Connect(context.Background()); c != nil || !errors.Is(e, ErrTLSRequired) {
		t.Fatal("VC bypassed cleartext credential gate", e)
	}
}

func TestOBProxyConnectCancellation(t *testing.T) {
	spec := obProxyTestSpec(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	r := &obProxyRuntime{initialize: func(obProxySpec) error { close(entered); <-release; return nil }}
	var credentials atomic.Int32
	created, err := NewConnector(Config{Network: networkUnix, Addr: spec.socket, DialContext: r.dialer(spec),
		Credentials: func(context.Context) (Credentials, error) {
			credentials.Add(1)
			return Credentials{User: testUser, Password: testPassword}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wait, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		c, e := created.Connect(wait)
		if c != nil {
			t.Error("canceled native initialization returned a connection")
			_ = c.Close()
		}
		done <- e
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stop()
	if err = <-done; !errors.Is(err, context.Canceled) || credentials.Load() != 0 {
		t.Fatal("native initialization cancellation reached database authentication", err)
	}
	once.Do(func() { close(release) })
	if err = r.start(ctx, spec); err != nil {
		t.Fatal(err)
	}
}
