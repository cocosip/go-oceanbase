package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const cancelInsecureTLSPath = "insecure-tls"

func armedCancelTarget(t *testing.T) (*conn, CancelTarget) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	c := &conn{cfg: Config{Network: networkTCP, Addr: "original:2881"},
		session: &handshakeSession{raw: client, user: testUser, mode: ModeMySQL, hello: wire.Handshake{ConnectionID: 42}}}
	target := c.queryTargetLocked()
	if !c.beginCancelExecution() {
		t.Fatal("target not armed")
	}
	return c, target
}

func TestCancelQueryVerifiedTLS(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, target := armedCancelTarget(t)
	target.identity.tlsVerified = true
	clientCfg, serverCfg := testTLSConfig(t)
	configured, err := NewConnector(Config{User: testUser, TLSConfig: clientCfg, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return routingTLSPeer(dialCtx, t, func(server net.Conn) {
			p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
			greeting := authGreeting(authNative, true)
			binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], 43)
			if !sendPackets(ctx, t, p, greeting) || !readAuthResponse(ctx, t, p) {
				return
			}
			secure := tls.Server(server, serverCfg)
			if e := secure.HandshakeContext(ctx); e != nil {
				t.Error(e)
				return
			}
			if e := p.ReplaceConn(authPeerConn{secure}); e != nil {
				t.Error(e)
				return
			}
			if !readAuthResponse(ctx, t, p) || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 2) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, configured, target); err != nil || c.closed.Load() {
		t.Fatal(err)
	}
}

func TestCancelQueryResetAndClose(t *testing.T) {
	for _, name := range []string{"reset", "close"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			configured, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := configured.Connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			c := raw.(*conn)
			defer func() { _ = c.Close() }()
			target := c.queryTargetLocked()
			if name == "reset" {
				err = c.ResetSession(ctx)
			} else {
				err = c.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.beginCancelExecution() {
				t.Fatal("target survived session boundary")
			}
			if err = CancelQuery(ctx, configured, target); !errors.Is(err, ErrResource) {
				t.Fatal(err)
			}
		})
	}
}

func TestCancelQueryGuards(t *testing.T) {
	for _, name := range []string{"network", "expected-mode", "no-tls", cancelInsecureTLSPath, "closed-connector", "closed-session", "inactive", "retired", "superseded", "pending"} {
		t.Run(name, func(t *testing.T) {
			c, target := armedCancelTarget(t)
			var dials atomic.Int32
			cfg := Config{User: testUser, DialContext: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected dial")
			}}
			switch name {
			case "network":
				cfg.Network, cfg.Addr = networkUnix, "/socket"
			case "expected-mode":
				cfg.Mode = ModeOracle
			case "no-tls", cancelInsecureTLSPath:
				target.identity.tlsVerified = true
				if name == cancelInsecureTLSPath {
					cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true} // Test rejects this policy before any dial.
				}
			case "closed-session":
				c.discard()
			case "inactive":
				c.cancellation.active = false
			case "retired":
				c.endCancelExecution(true)
			case "superseded":
				_ = c.queryTargetLocked()
			case "pending":
				c.cancellation.pending = &cancelAttempt{}
				c.cancellation.attempted = true
			}
			configured, err := NewConnector(cfg)
			if err != nil {
				if name == cancelInsecureTLSPath {
					return
				}
				t.Fatal(err)
			}
			if name == "closed-connector" {
				_ = configured.(*connector).Close()
			}
			if err = CancelQuery(context.Background(), configured, target); err == nil || dials.Load() != 0 {
				t.Fatalf("guard dialed=%d error=%v", dials.Load(), err)
			}
		})
	}
}

func TestCancelQueryAckCancellation(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, target := armedCancelTarget(t)
	killCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !serveBaseline(ctx, t, p, 2) || !readQuery(ctx, t, p, "KILL QUERY 42") {
			return
		}
		if _, e := p.ReadMessage(ctx); e == nil {
			t.Error("canceled helper did not close")
		}
	})
	configured, err := NewConnector(Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, e := peer(ctx, network, addr)
		return cancelAfterKillWrite{Conn: raw, cancel: cancel}, e
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(killCtx, configured, target); !errors.Is(err, context.Canceled) || !c.closed.Load() {
		t.Fatalf("unconfirmed KILL left original valid=%v: %v", !c.closed.Load(), err)
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

func TestCancelQueryAuxIdentity(t *testing.T) {
	for _, name := range []string{"user", "actual-mode", "same-id"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			c, target := armedCancelTarget(t)
			id, status := uint32(43), byte(2)
			cfg := Config{User: testUser}
			switch name {
			case "user":
				cfg.User = changeUserName
			case "same-id":
				id = 42
			default:
				status = 6
			}
			cfg.DialContext = peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !serveCancelConnect(ctx, t, p, status, id) {
					return
				}
				if name == "actual-mode" {
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("mode mismatch sent initialization or KILL")
					}
					return
				}
				if !serveBaseline(ctx, t, p, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			configured, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = CancelQuery(ctx, configured, target); err == nil || c.closed.Load() {
				t.Fatalf("aux mismatch affected original: closed=%v error=%v", c.closed.Load(), err)
			}
		})
	}
}

func TestCancelQueryFixedEntry(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, target := armedCancelTarget(t)
	peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	var dials atomic.Int32
	var diagnostics atomic.Int32
	configured, err := NewConnector(Config{User: testUser, Addr: "other:2881", Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: "another:2881"}}},
		Diagnostics: DiagnosticOptions{Request: func(context.Context, RequestDiagnostic) { diagnostics.Add(1) }},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if network != networkTCP || addr != "original:2881" {
				t.Errorf("cancel rerouted to %s/%s", network, addr)
			}
			return peer(ctx, network, addr)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, configured, target); err != nil || c.closed.Load() || dials.Load() != 1 {
		t.Fatal(err, dials.Load())
	}
	if diagnostics.Load() != 0 {
		t.Fatal("private auxiliary diagnostics can reenter the waiting original connection")
	}
	if err = CancelQuery(ctx, configured, target); !errors.Is(err, ErrResource) || dials.Load() != 1 {
		t.Fatalf("target replayed KILL: dials=%d error=%v", dials.Load(), err)
	}
	c.endCancelExecution(false)
}

func TestCancelQueryWaitsForAck(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	c, target := armedCancelTarget(t)
	killSent, ack := make(chan struct{}), make(chan struct{})
	configured, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCancelConnect(ctx, t, p, 2, 43) || !serveBaseline(ctx, t, p, 2) || !readQuery(ctx, t, p, "KILL QUERY 42") {
			return
		}
		close(killSent)
		select {
		case <-ack:
		case <-ctx.Done():
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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
	go func() { c.endCancelExecution(false); close(released) }()
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
	if c.beginCancelExecution() {
		t.Fatal("old target rearmed for the next SQL")
	}
}
