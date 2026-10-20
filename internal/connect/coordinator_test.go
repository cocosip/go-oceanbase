package connect

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestConnectBudget(t *testing.T) {
	for _, stage := range []string{"dial", credentialsStage, "greeting", tlsStage, "init"} {
		t.Run(stage, func(t *testing.T) {
			var deadline time.Time
			cfg := config.Config{User: testUser, ConnectTimeout: 70 * time.Millisecond}
			cfg.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				deadline, _ = ctx.Deadline()
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if stage != "dial" {
				if stage == credentialsStage {
					cfg.Credentials = func(ctx context.Context) (config.Credentials, error) {
						d, _ := ctx.Deadline()
						if d != deadline {
							t.Error("credentials received new budget")
						}
						<-ctx.Done()
						return config.Credentials{}, ctx.Err()
					}
				}
				if stage == tlsStage {
					cfg.TLSConfig, _ = testTLSConfig(t)
				}
				dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if stage == "greeting" {
						<-ctx.Done()
						return
					}
					greeting := authGreeting(authentication.Native, stage == tlsStage)
					if err := p.WriteMessage(ctx, greeting); err != nil {
						t.Error(err)
						return
					}
					if stage == credentialsStage {
						<-ctx.Done()
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					if stage == tlsStage {
						<-ctx.Done()
						return
					}
					if err := p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
						t.Error(err)
						return
					}
					p.ResetSequence()
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					<-ctx.Done()
				})
				cfg.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
					deadline, _ = ctx.Deadline()
					return dial(ctx, n, a)
				}
			}
			c, err := newConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = c.Connect(context.Background())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stage %s escaped total budget: %v", stage, err)
			}
			if deadline.IsZero() || time.Since(start) > time.Second {
				t.Fatal("unbounded Connect")
			}
		})
	}
}

func TestConnectorModePinning(t *testing.T) {
	var attempts atomic.Int32
	c, err := newConnector(config.Config{User: testUser, Session: config.SessionOptions{InitSQL: []string{readySQL}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		attempt := attempts.Add(1)
		status := byte(6)
		if attempt == 1 {
			status = 2
		}
		if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err != nil || string(data) != "\x03SET @ready=1" {
			t.Errorf("init SQL: %q %v", data, err)
			return
		}
		response := []byte{0, 0, 0, status, 0, 0, 0}
		if attempt == 1 {
			response = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'n', 'o'}
		}
		if err := p.WriteMessage(ctx, response); err != nil {
			t.Error(err)
			return
		}
		p.ResetSequence()
		if attempt == 1 {
			if _, err := p.ReadMessage(ctx); err == nil {
				t.Error("failed init kept connection")
			}
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Connect(context.Background()); err == nil {
		t.Fatal("initialization failure hidden")
	}
	// Oracle must succeed even though the first MySQL authentication succeeded.
	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("failed initialization pinned mode: %v", err)
	}
	if err = closePhysical(conn); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorConcurrentModes(t *testing.T) {
	var mu sync.Mutex
	next := byte(2)
	initialized := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	c, err := newConnector(config.Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		mu.Lock()
		status := next
		next = 6
		mu.Unlock()
		if !serveConnect(ctx, t, p, status) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		initialized <- struct{}{}
		<-release
		if err := p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
			t.Error(err)
			return
		}
		if !serveVersion(ctx, t, p, status, false) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		p.ResetSequence()
		data, err := p.ReadMessage(context.Background())
		if err == nil {
			if !bytes.Equal(data, []byte{1}) {
				t.Error("unexpected command")
			}
			if _, err = p.ReadMessage(context.Background()); err == nil {
				t.Error("missing transport close")
			}
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			conn, err := c.Connect(context.Background())
			if err == nil {
				err = closePhysical(conn)
			}
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-initialized:
		case <-time.After(time.Second):
			t.Fatal("Connect serialized initialization or failed")
		}
	}
	releaseOnce.Do(func() { close(release) })
	passed, failed := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			passed++
		} else if errors.Is(err, dberrors.ErrModeMismatch) {
			failed++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || failed != 1 {
		t.Fatalf("mixed pool modes: %d %d", passed, failed)
	}
}

func TestConnectConfigValidation(t *testing.T) {
	for _, session := range []config.SessionOptions{
		{Variables: map[string]string{"a; SELECT 1": "value"}},
		{Variables: map[string]string{"a": "bad\x00value"}},
		{InitSQL: []string{"bad\x00SQL"}},
	} {
		if _, err := newConnector(config.Config{User: testUser, Session: session}); err == nil {
			t.Fatal("invalid initialization configuration accepted")
		}
	}
}

func TestConnectOceanBaseVersion(t *testing.T) {
	c, err := newConnector(config.Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closePhysical(created) }()
	if physicalVersion(t, created) != "4.3.5.1" {
		t.Fatal("OceanBase version was inferred from MySQL greeting instead of server query")
	}
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
	c, err := newConnector(config.Config{User: testUser, ConnectTimeout: time.Second, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, AttemptTimeout: 30 * time.Millisecond, RetryDelay: time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
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
	if err = closePhysical(raw); err != nil {
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
			o := config.RoutingOptions{MaxAttempts: 3, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond}
			if cancelled {
				o.RetryDelay = time.Hour
			}
			c, err := newConnector(config.Config{User: testUser, Routing: o, DialContext: func(context.Context, string, string) (net.Conn, error) {
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
	c, err := newConnector(config.Config{User: testUser, ConnectTimeout: 50 * time.Millisecond, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 100, AttemptTimeout: 10 * time.Millisecond, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
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
			cfg := config.Config{User: testUser, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 4, RetryDelay: time.Millisecond}}
			if stage == failoverModeStage {
				cfg.Mode = config.ModeOracle
			}
			if stage == credentialsStage {
				cfg.Credentials = func(context.Context) (config.Credentials, error) {
					return config.Credentials{}, context.DeadlineExceeded
				}
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
					if !sendPackets(ctx, t, p, authGreeting(authentication.Native, false)) {
						return
					}
					_, _ = p.ReadMessage(ctx)
					return
				}
				if stage == "auth" {
					if !sendPackets(ctx, t, p, authGreeting(authentication.Native, false)) {
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
			c, err := newConnector(cfg)
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
	c, err := newConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = closePhysical(raw); err != nil {
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
			cfg := config.Config{User: testUser, TLSConfig: clientCfg, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: second}}, MaxAttempts: 3, RetryDelay: time.Millisecond}}
			if path == routeTransportDisconnect {
				cfg.Routing.Endpoints = []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}
			}
			cfg.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
				attempt := calls.Add(1)
				got = append(got, addr)
				if attempt == 1 && path != routeTransportDisconnect {
					return nil, &net.OpError{Op: routeDialOperation, Net: "tcp", Err: errors.New("refused")}
				}
				return routingTLSPeer(ctx, t, func(server net.Conn) {
					p := wire.NewPacketIO(authPeerConn{Conn: server}, 64<<20)
					if !sendPackets(ctx, t, p, authGreeting(authentication.Native, true)) {
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
					if err := p.ReplaceConn(authPeerConn{Conn: secure}); err != nil {
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
			c, err := newConnector(cfg)
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
			if err = closePhysical(raw); err != nil {
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
	c, err := newConnector(config.Config{User: testUser, ConnectTimeout: time.Second, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, AttemptTimeout: 20 * time.Millisecond}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { calls.Add(1); return dial(ctx, n, a) }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Connect(context.Background())
	if raw != nil {
		_ = closePhysical(raw)
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
			cfg := config.Config{User: testUser, TLSConfig: clientCfg, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Millisecond}}
			cfg.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				calls.Add(1)
				return routingTLSPeer(ctx, t, func(server net.Conn) {
					p := wire.NewPacketIO(authPeerConn{Conn: server}, 64<<20)
					if !sendPackets(ctx, t, p, authGreeting(authentication.Native, true)) {
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
			c, err := newConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Connect(context.Background()); err == nil || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestFailoverBudget(t *testing.T) {
	var addresses []string
	var deadlines []time.Time
	good := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	connector, err := newConnector(config.Config{User: testUser, ConnectTimeout: time.Second, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		addresses = append(addresses, addr)
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		if addr == routeFirst {
			return nil, &net.OpError{Op: routeDialOperation, Net: network, Err: errors.New("refused")}
		}
		return good(ctx, network, addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = closePhysical(c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(addresses, []string{routeFirst, routeSecond}) || deadlines[1].Before(deadlines[0]) {
		t.Fatal(addresses, deadlines)
	}
}

func TestRoleIsolation(t *testing.T) {
	endpoints := []config.Endpoint{{Addr: routeFirst, Role: config.RoleReadWrite}, {Addr: routeSecond, Role: config.RoleReadOnly}}
	for _, role := range []config.EndpointRole{config.RoleReadWrite, config.RoleReadOnly} {
		var addresses []string
		connector, err := newConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Endpoints: endpoints, Role: role}, DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			addresses = append(addresses, addr)
			return nil, errors.New("dial failure")
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = connector.Connect(context.Background())
		want := routeFirst
		if role == config.RoleReadOnly {
			want = routeSecond
		}
		if !reflect.DeepEqual(addresses, []string{want}) {
			t.Fatal(role, addresses)
		}
	}
	if _, err := newConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Role: config.RoleReadOnly, Endpoints: []config.Endpoint{{Addr: routeFirst, Role: config.RoleReadWrite}}}}); err == nil {
		t.Fatal("missing role accepted")
	}
}

func TestDiscoveryConnectBudget(t *testing.T) {
	c, err := newConnector(config.Config{User: testUser, ConnectTimeout: 20 * time.Millisecond, Discovery: config.DiscoveryOptions{Service: discoveryTestService, RequestTimeout: time.Second, Source: func(ctx context.Context) (config.Topology, error) { <-ctx.Done(); return config.Topology{}, ctx.Err() }}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = c.Connect(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("lookup exceeded connection budget")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverySources(t *testing.T) {
	path := filepath.Join(t.TempDir(), discoveryTestFile)
	if err := os.WriteFile(path, []byte(discoveryTestAlias+discoveryDescriptor), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := config.NewTNSFileSource(path, discoveryTestService)
	if err != nil {
		t.Fatal(err)
	}
	topology, err := source(context.Background())
	if err != nil || topology.Service != discoveryTestService || len(topology.Endpoints) != 1 {
		t.Fatalf("TNS: %+v %v", topology, err)
	}
	if err = os.WriteFile(path, []byte(discoveryTestAlias+discoveryDescriptor+"broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = source(context.Background()); err == nil {
		t.Fatal("accepted invalid refresh")
	}
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		if r.URL.Path == "/target" {
			redirected.Add(1)
		}
		_, _ = w.Write([]byte(`{"ADDRESS_LIST":[{"ADDRESS":[{"HOST":"first","TYPE":"master"}]}],"CONNECT_DATA":{"SERVICE_NAME":"orders"}}`))
	}))
	defer server.Close()
	source, err = config.NewOCPSource(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if topology, err = source(context.Background()); err != nil || topology.Endpoints[0].Addr != discoveryTestAddr {
		t.Fatalf("OCP: %+v %v", topology, err)
	}
	source, err = config.NewOCPSource(server.URL+"/redirect", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestDiscoveryDSN(t *testing.T) {
	query := url.Values{"tnsFile": {discoveryTestFile}, "tnsAlias": {discoveryTestService}, "discoveryService": {discoveryTestService}, "discoveryRefreshInterval": {"1s"}, "discoveryTimeout": {"2s"}, "discoveryMaxAge": {"5s"}}
	cfg, err := config.ParseDSN("oceanbase://user@localhost/?" + query.Encode())
	if err != nil || cfg.Discovery.Source == nil || cfg.Discovery.RefreshInterval != time.Second || cfg.Routing.MaxAttempts != 0 || cfg.Routing.AttemptTimeout != 0 {
		t.Fatalf("DSN: %+v %v", cfg.Discovery, err)
	}
	for _, q := range []string{"tnsFile=x", "ocpURL=http://localhost", "discoveryService=orders", "tnsFile=x&tnsAlias=orders&ocpURL=http://localhost&discoveryService=orders", "ocpURL=http://user:secret@localhost&discoveryService=orders", "discoveryTimeout=-1s", "addressDescription=" + url.QueryEscape(discoveryDescriptor) + "&addresses=other"} {
		if _, err = config.ParseDSN("oceanbase://user@localhost/?" + q); err == nil {
			t.Fatal("accepted ambiguous discovery config")
		}
	}
	cfg, err = config.ParseDSN("oceanbase://user@localhost/?addressDescription=" + url.QueryEscape(discoveryDescriptor))
	if err != nil || len(cfg.Routing.Endpoints) != 1 || cfg.Routing.Endpoints[0].Addr != discoveryTestAddr || cfg.Routing.Endpoints[0].Role != config.RoleReadWrite {
		t.Fatalf("descriptor: %+v %v", cfg.Routing, err)
	}
}

func TestTNSFileRequiresSelectedAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), discoveryTestFile)
	for _, content := range []string{discoveryDescriptor, "other=" + discoveryDescriptor, "other=" + discoveryDescriptor + "\n" + discoveryDescriptor} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := config.NewTNSFileSource(path, discoveryTestService)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = source(context.Background()); err == nil {
			t.Fatal("missing file alias silently replaced by inline description")
		}
	}
}

func TestTNSFileFollowsRegularTarget(t *testing.T) {
	dir := t.TempDir()
	path, link := filepath.Join(dir, "target.ob"), filepath.Join(dir, discoveryTestFile)
	if err := os.WriteFile(path, []byte(discoveryTestAlias+discoveryDescriptor), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Skip("symbolic links unavailable")
	}
	source, err := config.NewTNSFileSource(link, discoveryTestService)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := source(context.Background()); err != nil || s.Service != discoveryTestService {
		t.Fatalf("regular target: %+v %v", s, err)
	}
}

func TestReadTimeoutConnect(t *testing.T) {
	for _, stage := range []string{"server-hello", "initialization"} {
		t.Run(stage, func(t *testing.T) {
			cfg := config.Config{User: testUser, ReadTimeout: 50 * time.Millisecond}
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if stage == "initialization" && (!serveConnect(ctx, t, p, 2) || !readQuery(ctx, t, p, "SET NAMES utf8mb4")) {
					return
				}
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("stalled connection sent an extra command")
				}
			})
			configured, err := newConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = configured.Connect(ctx)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("connection ignored read timeout", err)
			}
		})
	}
}

var peerDial = testpeer.PeerDial

var serveConnect = testpeer.ServeConnect

var serveQuit = testpeer.ServeQuit

var serveOK = testpeer.ServeOK

var serveBaseline = testpeer.ServeBaseline

var serveVersion = testpeer.ServeVersion

var readQuery = testpeer.ReadQuery

var routingTLSPeer = testpeer.RoutingTLSPeer

const credentialsStage = "credentials"

const readySQL = "SET @ready=1"

const tlsStage = "tls"

const routeDialOperation = "connect"

const networkTCP = "tcp"

const failoverModeStage = "mode-reject"

const routeCertificateReject = "certificate-reject"

const routeTransportDisconnect = "transport-disconnect"

const testOutInit = "init"

const discoveryTestFile = "tnsnames.ob"

const discoveryTestService = "orders"

const discoveryTestAlias = "orders="

const discoveryTestAddr = "first:2881"

const discoveryDescriptor = `(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(HOST=first)(TYPE=master)))(CONNECT_DATA=(SERVICE_NAME=orders)))`

func closePhysical(physical *session.Session) error {
	ctx, stop := context.WithTimeout(context.Background(), physical.Config().Limits.CleanupTimeout)
	defer stop()
	op, err := physical.Acquire(ctx)
	if err != nil {
		physical.Discard()
		return err
	}
	_, err = command.Start(ctx, op, []byte{1}, false)
	op.EndResponse()
	physical.Discard()
	op.Release()
	return err
}

func physicalVersion(t *testing.T, physical *session.Session) string {
	t.Helper()
	op, err := physical.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	return op.Protocol().OBVersion
}
