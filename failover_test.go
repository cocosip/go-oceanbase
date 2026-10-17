package oceanbase

import (
	"context"
	"crypto/tls"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const routeDialOperation = "connect"
const failoverModeStage = "mode-reject"
const routeCertificateReject = "certificate-reject"
const routeTransportDisconnect = "transport-disconnect"

// TLS alerts and application writes need a buffered, full-duplex transport.
func routingTLSPeer(ctx context.Context, t *testing.T, serve func(net.Conn)) (net.Conn, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()
	client, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		return nil, err
	}
	server, err := listener.Accept()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err = server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() { defer close(done); defer func() { _ = server.Close() }(); serve(server) }()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = server.Close()
			<-done
		}
	})
	return client, nil
}

func TestFailoverAttemptTimeout(t *testing.T) {
	var dials atomic.Int32
	good := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c, err := NewConnector(Config{User: testUser, ConnectTimeout: time.Second, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, AttemptTimeout: 30 * time.Millisecond, RetryDelay: time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		dials.Add(1)
		if a == routeFirst {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return good(ctx, n, a)
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 2 {
		t.Fatal(dials.Load())
	}
}

func TestFailoverAllDown(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "attempt-limit", true: "cancel-backoff"}[cancelled], func(t *testing.T) {
			var calls atomic.Int32
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			injected := &net.OpError{Op: routeDialOperation, Net: networkTCP, Err: errors.New("refused")}
			o := RoutingOptions{MaxAttempts: 3, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond}
			if cancelled {
				o.RetryDelay = time.Hour
			}
			c, err := NewConnector(Config{User: testUser, Routing: o, DialContext: func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				if cancelled {
					stop()
				}
				return nil, injected
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Connect(ctx)
			want := int32(3)
			if cancelled {
				want = 1
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if calls.Load() != want {
				t.Fatal(calls.Load(), want)
			}
		})
	}
}

func TestFailoverTotalDeadline(t *testing.T) {
	var calls atomic.Int32
	c, err := NewConnector(Config{User: testUser, ConnectTimeout: 50 * time.Millisecond, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 100, AttemptTimeout: 10 * time.Millisecond, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.Connect(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || calls.Load() > 6 {
		t.Fatal(err, calls.Load())
	}
}

func TestFailoverTerminalErrors(t *testing.T) {
	for _, stage := range []string{"auth", failoverModeStage, "short-greeting", credentialsStage, testOutInit} {
		t.Run(stage, func(t *testing.T) {
			var calls atomic.Int32
			cfg := Config{User: testUser, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 4, RetryDelay: time.Millisecond}}
			if stage == failoverModeStage {
				cfg.Mode = ModeOracle
			}
			if stage == credentialsStage {
				cfg.Credentials = func(context.Context) (Credentials, error) { return Credentials{}, context.DeadlineExceeded }
			}
			if stage == testOutInit {
				cfg.Session.InitSQL = []string{readySQL}
			}
			dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if stage == "short-greeting" {
					_ = p.WriteMessage(ctx, []byte{10})
					return
				}
				if stage == credentialsStage {
					if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
						return
					}
					_, _ = p.ReadMessage(ctx)
					return
				}
				if stage == "auth" {
					if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					_ = p.WriteMessage(ctx, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'n', 'o'})
					return
				}
				if !serveConnect(ctx, t, p, 2) {
					return
				}
				if stage == failoverModeStage {
					p.ResetSequence()
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("mode mismatch sent initialization")
					}
					return
				}
				if !serveBaseline(ctx, t, p, 2) {
					return
				}
				readQuery(ctx, t, p, readySQL) // Lose response after caller initialization; never replay it.
			})
			cfg.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }
			c, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Connect(context.Background()); err == nil || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestFailoverHandshakeDisconnect(t *testing.T) {
	var calls atomic.Int32
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if calls.Load() == 1 {
			return
		}
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}

func TestFailoverTLS(t *testing.T) {
	for _, path := range []string{"selected-host", routeCertificateReject, routeTransportDisconnect} {
		t.Run(path, func(t *testing.T) {
			clientCfg, serverCfg := testTLSConfig(t)
			clientCfg.ServerName = ""
			clientCfg.MaxVersion = tls.VersionTLS12
			serverCfg.MaxVersion = tls.VersionTLS12
			var calls atomic.Int32
			var got []string
			second := testServerName + ":2881"
			if path == routeCertificateReject {
				second = routeSecond
			}
			if path == routeTransportDisconnect {
				second = routeFirst
				clientCfg.ServerName = testServerName
			}
			cfg := Config{User: testUser, TLSConfig: clientCfg, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: second}}, MaxAttempts: 3, RetryDelay: time.Millisecond}}
			if path == routeTransportDisconnect {
				cfg.Routing.Endpoints = []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}
			}
			cfg.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
				attempt := calls.Add(1)
				got = append(got, addr)
				if attempt == 1 && path != routeTransportDisconnect {
					return nil, &net.OpError{Op: routeDialOperation, Net: "tcp", Err: errors.New("refused")}
				}
				return routingTLSPeer(ctx, t, func(server net.Conn) {
					p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
					if !sendPackets(ctx, t, p, authGreeting(authNative, true)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					if attempt == 1 && path == routeTransportDisconnect {
						return
					}
					secure := tls.Server(server, serverCfg)
					if err := secure.HandshakeContext(ctx); err != nil {
						if path != routeCertificateReject {
							t.Error(err)
						}
						return
					}
					if err := p.ReplaceConn(authPeerConn{secure}); err != nil {
						t.Error(err)
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveBaseline(ctx, t, p, 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
			}
			c, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := c.Connect(context.Background())
			if path == routeCertificateReject {
				if err == nil || calls.Load() != 2 {
					t.Fatal(err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, []string{routeFirst, second}) && path != routeTransportDisconnect {
				t.Fatal(got)
			}
			if calls.Load() != 2 {
				t.Fatal(calls.Load())
			}
		})
	}
}

func TestRoleTransactionSocket(t *testing.T) {
	var dials atomic.Int32
	created, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Role: RoleReadOnly}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		dials.Add(1)
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !serveOK(ctx, t, p, []byte("\x03START TRANSACTION READ ONLY"), 3) || !serveOK(ctx, t, p, []byte("\x03ROLLBACK"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := created.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := raw.(driver.ConnBeginTx).BeginTx(context.Background(), driver.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 1 {
		t.Fatal(dials.Load())
	}
}

func TestFailoverInitializationTimeout(t *testing.T) {
	var calls atomic.Int32
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !readQuery(ctx, t, p, "SET NAMES utf8mb4") {
			return
		}
		<-ctx.Done()
		// Sending after the attempt expires must not allow initialization to continue.
		if err := p.WriteMessage(context.Background(), []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
			return
		}
		if !serveOK(context.Background(), t, p, []byte("\x03SET autocommit=1"), 2) || !serveVersion(context.Background(), t, p, 2, false) || !serveOK(context.Background(), t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c, err := NewConnector(Config{User: testUser, ConnectTimeout: time.Second, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, AttemptTimeout: 20 * time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Connect(context.Background())
	if raw != nil {
		_ = raw.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
}

func TestFailoverClientCertificateReject(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(map[uint16]string{tls.VersionTLS12: "tls12", tls.VersionTLS13: "tls13"}[version], func(t *testing.T) {
			clientCfg, serverCfg := testTLSConfig(t)
			clientCfg.MinVersion, clientCfg.MaxVersion = version, version
			serverCfg.MinVersion, serverCfg.MaxVersion = version, version
			serverCfg.ClientAuth = tls.RequireAndVerifyClientCert
			var calls atomic.Int32
			cfg := Config{User: testUser, TLSConfig: clientCfg, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Millisecond}}
			cfg.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				calls.Add(1)
				return routingTLSPeer(ctx, t, func(server net.Conn) {
					p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
					if !sendPackets(ctx, t, p, authGreeting(authNative, true)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					secure := tls.Server(server, serverCfg)
					if err := secure.HandshakeContext(ctx); err == nil {
						t.Error("client without certificate accepted")
					} else {
						// Drain pending application writes so TCP close preserves the TLS alert.
						_, _ = io.Copy(io.Discard, server)
					}
				})
			}
			c, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Connect(context.Background()); err == nil || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}
