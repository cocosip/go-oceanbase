package control

import (
	bytes "bytes"
	context "context"
	tls "crypto/tls"
	binary "encoding/binary"
	errors "errors"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"

	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	cursor "github.com/cocosip/go-oceanbase/internal/cursor"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	session "github.com/cocosip/go-oceanbase/internal/session"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCancelQueryVerifiedTLS(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, _, target := armedCancelTarget(t, true)
	clientCfg, serverCfg := testpeer.TLSConfig(t)
	configured, err := controlConnector(config.Config{User: testUser, TLSConfig: clientCfg, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return testpeer.RoutingTLSPeer(dialCtx, t, func(server net.Conn) {
			p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
			greeting := testpeer.AuthGreeting(authentication.Native, true)
			binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], 43)
			if !testpeer.SendPackets(ctx, t, p, greeting) || !testpeer.ReadAuthResponse(ctx, t, p) {
				return
			}
			secure := tls.Server(server, serverCfg)
			if e := secure.HandshakeContext(ctx); e != nil {
				t.Error(e)
				return
			}
			if e := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); e != nil {
				t.Error(e)
				return
			}
			if !testpeer.ReadAuthResponse(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 2) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, configured, target); err != nil || c.Closed() {
		t.Fatal(err)
	}
}

func TestCancelQueryAckCancellation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, _, target := armedCancelTarget(t)
	killCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ReadQuery(ctx, t, p, "KILL QUERY 42") {
			return
		}
		if _, e := p.ReadMessage(ctx); e == nil {
			t.Error("canceled helper did not close")
		}
	})
	configured, err := controlConnector(config.Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, e := peer(ctx, network, addr)
		return cancelAfterKillWrite{Conn: raw, cancel: cancel}, e
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(killCtx, configured, target); !errors.Is(err, context.Canceled) || !c.Closed() {
		t.Fatalf("unconfirmed KILL left original valid=%v: %v", !c.Closed(), err)
	}
}

func TestCancelQueryAuxIdentity(t *testing.T) {
	for _, name := range []string{"user", "actual-mode", "same-id"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			c, _, target := armedCancelTarget(t)
			id, status := uint32(43), byte(2)
			cfg := config.Config{User: testUser}
			switch name {
			case "user":
				cfg.User = changeUserName
			case "same-id":
				id = 42
			default:
				status = 6
			}
			cfg.DialContext = testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !serveCancelConnect(ctx, t, p, status, id) {
					return
				}
				if name == "actual-mode" {
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("mode mismatch sent initialization or KILL")
					}
					return
				}
				if !testpeer.ServeBaseline(ctx, t, p, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			configured, err := controlConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = CancelQuery(ctx, configured, target); err == nil || c.Closed() {
				t.Fatalf("aux mismatch affected original: closed=%v error=%v", c.Closed(), err)
			}
		})
	}
}

func TestCancelQueryFixedEntry(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, _, target := armedCancelTarget(t)
	peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	var dials atomic.Int32
	var diagnostics atomic.Int32
	configured, err := controlConnector(config.Config{User: testUser, Addr: "other:2881", Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: "another:2881"}}},
		Diagnostics: config.DiagnosticOptions{Request: func(context.Context, config.RequestDiagnostic) { diagnostics.Add(1) }},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if network != cancelNetworkTCP || addr != cancelEntryAddress {
				t.Errorf("cancel rerouted to %s/%s", network, addr)
			}
			return peer(ctx, network, addr)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, configured, target); err != nil || c.Closed() || dials.Load() != 1 {
		t.Fatal(err, dials.Load())
	}
	if diagnostics.Load() != 0 {
		t.Fatal("private auxiliary diagnostics can reenter the waiting original connection")
	}
	if err = CancelQuery(ctx, configured, target); !errors.Is(err, dberrors.ErrResource) || dials.Load() != 1 {
		t.Fatalf("target replayed KILL: dials=%d error=%v", dials.Load(), err)
	}
	c.EndCancellation(false)
}

func TestCancelQueryWaitsForAck(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, _, target := armedCancelTarget(t)
	killSent, ack := make(chan struct{}), make(chan struct{})
	configured, err := controlConnector(config.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ReadQuery(ctx, t, p, "KILL QUERY 42") {
			return
		}
		close(killSent)
		select {
		case <-ack:
		case <-ctx.Done():
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- CancelQuery(ctx, configured, target) }()
	select {
	case <-killSent:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	released := make(chan struct{})
	go func() { c.EndCancellation(false); close(released) }()
	select {
	case <-released:
		t.Fatal("query completed before outstanding KILL confirmation")
	case <-time.After(20 * time.Millisecond):
	}
	close(ack)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if c.BeginCancellation() {
		t.Fatal("old target rearmed for the next SQL")
	}
}

type cancelAfterKillWrite struct {
	net.Conn
	cancel context.CancelFunc
}

func (c cancelAfterKillWrite) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil && bytes.Equal(data, []byte("\x03KILL QUERY 42")) {
		c.cancel()
	}
	return n, err
}

func serveCancelConnect(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, id uint32) bool {
	t.Helper()
	greeting := testpeer.AuthGreeting(authentication.Native, false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return testpeer.SendPackets(ctx, t, p, greeting) && testpeer.ReadAuthResponse(ctx, t, p) && testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0})
}

const cancelInsecureTLSPath = "insecure-tls"

func armedCancelTarget(t *testing.T, tlsVerified ...bool) (*session.Session, *session.Operation, session.Target) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	p := &session.Negotiated{Raw: client, User: testUser, Mode: config.ModeMySQL, Hello: wire.Handshake{ConnectionID: 42}}
	if len(tlsVerified) > 0 {
		p.TLSVerified = tlsVerified[0]
	}
	s, op := controlOperation(t, config.Config{Network: cancelNetworkTCP, Addr: cancelEntryAddress}, p)
	target := op.ArmTarget()
	if !s.BeginCancellation() {
		t.Fatal("target not armed")
	}
	return s, op, target
}

func TestCancelQueryGuards(t *testing.T) {
	for _, name := range []string{"network", "expected-mode", cancelNoTLSPath, cancelInsecureTLSPath, "closed-connector", "closed-session", "inactive", "retired", "superseded", "pending"} {
		t.Run(name, func(t *testing.T) {
			c, op, target := armedCancelTarget(t, name == cancelNoTLSPath || name == cancelInsecureTLSPath)
			var dials atomic.Int32
			cfg := config.Config{User: testUser, DialContext: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected dial")
			}}
			switch name {
			case "network":
				cfg.Network, cfg.Addr = "unix", "/socket"
			case "expected-mode":
				cfg.Mode = config.ModeOracle
			case cancelNoTLSPath, cancelInsecureTLSPath:
				if name == cancelInsecureTLSPath {
					cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true}
				}
			case "closed-session":
				c.Discard()
			case "inactive":
				target = op.ArmTarget()
			case "retired":
				c.EndCancellation(true)
			case "superseded":
				_ = op.ArmTarget()
			case "pending":
				pending, err := target.Begin(func() {})
				if err != nil {
					t.Fatal(err)
				}
				defer pending.Finish(false)
			}
			configured, err := controlConnector(cfg)
			if err != nil {
				if name == cancelInsecureTLSPath {
					return
				}
				t.Fatal(err)
			}
			if name == "closed-connector" {
				_ = configured.Close()
			}
			if err = CancelQuery(context.Background(), configured, target); err == nil || dials.Load() != 0 {
				t.Fatalf("guard dialed=%d error=%v", dials.Load(), err)
			}
		})
	}
}

func TestCancelQueryResetAndClose(t *testing.T) {
	for _, name := range []string{"reset", "close"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			configured, err := controlConnector(config.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			c, err := configured.Connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			op, err := c.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer op.Release()
			defer func() { _ = closeControlSession(ctx, c, op) }()
			target := op.ArmTarget()
			if name == "reset" {
				err = New().Reset(ctx, op, &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{})
			} else {
				err = closeControlSession(ctx, c, op)
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.BeginCancellation() {
				t.Fatal("target survived session boundary")
			}
			if err = CancelQuery(ctx, configured, target); !errors.Is(err, dberrors.ErrResource) {
				t.Fatal(err)
			}
		})
	}
}

const testUser = "app"
const changeUserName = "next"

const cancelEntryAddress = "original:2881"
const cancelNoTLSPath = "no-tls"

const cancelNetworkTCP = "tcp"
