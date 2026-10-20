package obproxy_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/obproxy"
)

const networkUnix = "unix"

func obProxyProtocolPeer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	listener, path := testpeer.ListenUnix(t, testpeer.UnixTempDir(t), "vc.sock")
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
	return path
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
		for _, framing := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
			t.Run(fmt.Sprintf("%d/%s", status, framing), func(t *testing.T) {
				var starts, closed atomic.Int32
				socket := obProxyProtocolPeer(t, func(server net.Conn) {
					if starts.Load() != 1 {
						t.Error("Unix dial bypassed native initialization")
					}
					p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
					ctx := context.Background()
					if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
						return
					}
					_ = testpeer.ReadCommand(ctx, t, p, []byte{1})
				})
				_, dial, _ := obproxy.NewTestRuntime(t, socket, func() error { starts.Add(1); return nil })
				cfg := oceanbase.Config{User: testpeer.User, Mode: testpeer.ModeFromStatus(status), Network: networkUnix, Addr: socket,
					DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
						c, e := dial(ctx, network, address)
						if e != nil {
							return nil, e
						}
						return obProxyCloseConn{Conn: c, closed: &closed}, nil
					}}
				if framing == testpeer.Compression {
					cfg.Protocol.Compression = oceanbase.FeatureRequired
				}
				if framing == testpeer.OB20 {
					cfg.Protocol.OB20 = oceanbase.FeatureRequired
				}
				created, err := oceanbase.NewConnector(cfg)
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
			clientTLS, serverTLS := testpeer.TLSConfig(t)
			socket := obProxyProtocolPeer(t, func(server net.Conn) {
				ctx := context.Background()
				p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
				if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(testpeer.AuthClear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
					return
				}
				secure := tls.Server(server, serverTLS)
				if e := secure.HandshakeContext(ctx); e != nil {
					t.Error(e)
					return
				}
				if e := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); e != nil {
					t.Error(e)
					return
				}
				if b, e := p.ReadMessage(ctx); e != nil || !strings.Contains(string(b), "vc-secret") {
					t.Errorf("missing TLS credentials: %x %v", b, e)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0}) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				_ = testpeer.ReadCommand(ctx, t, p, []byte{1})
			})
			_, dial, _ := obproxy.NewTestRuntime(t, socket, func() error { return nil })
			created, err := oceanbase.NewConnector(oceanbase.Config{User: testpeer.User, Password: "vc-secret", Mode: testpeer.ModeFromStatus(status), TLSConfig: clientTLS,
				Network: networkUnix, Addr: socket, DialContext: dial})
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
	socket := obProxyProtocolPeer(t, func(server net.Conn) {
		ctx := context.Background()
		p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(testpeer.AuthClear, false)) {
			return
		}
		if b, e := p.ReadMessage(ctx); e == nil || len(b) != 0 {
			t.Error("VC exposed cleartext credentials without verified TLS")
		}
	})
	_, dial, _ := obproxy.NewTestRuntime(t, socket, func() error { return nil })
	created, err := oceanbase.NewConnector(oceanbase.Config{Network: networkUnix, Addr: socket, DialContext: dial, User: testpeer.User, Password: testpeer.Password})
	if err != nil {
		t.Fatal(err)
	}
	if c, e := created.Connect(context.Background()); c != nil || !errors.Is(e, oceanbase.ErrTLSRequired) {
		t.Fatal("VC bypassed cleartext credential gate", e)
	}
}

func TestOBProxyConnectCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	socket, dial, start := obproxy.NewTestRuntime(t, "", func() error { close(entered); <-release; return nil })
	var credentials atomic.Int32
	created, err := oceanbase.NewConnector(oceanbase.Config{Network: networkUnix, Addr: socket, DialContext: dial,
		Credentials: func(context.Context) (oceanbase.Credentials, error) {
			credentials.Add(1)
			return oceanbase.Credentials{User: testpeer.User, Password: testpeer.Password}, nil
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
	if err = start(ctx); err != nil {
		t.Fatal(err)
	}
}
