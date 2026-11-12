package connect_test

import (
	"context"
	"database/sql"
	"net"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// loginPeer scripts one candidate. serve receives the address so a single
// DialContext can present a distinct endpoint per dial while staying in memory.
func loginPeer(t *testing.T, serve func(addr string, p *wire.PacketIO) bool) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		client, server := net.Pipe()
		if err := server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			serve(addr, wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20))
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}
}

// rejectAuth sends the greeting, reads the client's answer, then reports a
// login failure exactly as a server would.
func rejectAuth(ctx context.Context, t *testing.T, p *wire.PacketIO, packet []byte) bool {
	t.Helper()
	if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(testpeer.AuthNative, false)) {
		return false
	}
	if !testpeer.ReadAuthResponse(ctx, t, p) {
		return false
	}
	return testpeer.SendPackets(ctx, t, p, packet)
}

func newTwoEndpointConnector(t *testing.T, dial func(context.Context, string, string) (net.Conn, error), attempts int) *sql.DB {
	t.Helper()
	connector, err := oceanbase.NewConnector(config.Config{User: testUser, Routing: config.RoutingOptions{
		Endpoints:   []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}},
		MaxAttempts: attempts,
	}, DialContext: dial})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}

// errPacket builds a MySQL ERR packet with the given vendor code and SQLState.
func errPacket(code uint16, state string, message string) []byte {
	packet := []byte{255, byte(code), byte(code >> 8), '#'}
	packet = append(packet, state...)
	return append(packet, message...)
}

// A per-endpoint overload must fall through to the next candidate instead of
// failing the whole connector.
func TestLoginOverloadSwitchesCandidate(t *testing.T) {
	var dials atomic.Int32
	dial := loginPeer(t, func(addr string, p *wire.PacketIO) bool {
		dials.Add(1)
		ctx := context.Background()
		if addr == routeFirst {
			return rejectAuth(ctx, t, p, errPacket(1040, "08004", "Too many connections"))
		}
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return false
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
		return true
	})
	db := newTwoEndpointConnector(t, dial, 3)
	defer func() { _ = db.Close() }()
	raw, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("overloaded candidate aborted failover: %v", err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2", got)
	}
}

// A rejected credential belongs to the whole deployment, so the first endpoint
// must end the candidate loop immediately.
func TestLoginRejectionStopsCandidates(t *testing.T) {
	var dials atomic.Int32
	dial := loginPeer(t, func(addr string, p *wire.PacketIO) bool {
		dials.Add(1)
		if addr != routeFirst {
			t.Error("credential rejection reached the next candidate")
			return false
		}
		return rejectAuth(context.Background(), t, p, errPacket(1045, "28000", "Access denied"))
	})
	db := newTwoEndpointConnector(t, dial, 3)
	defer func() { _ = db.Close() }()
	if _, err := db.Conn(context.Background()); err == nil {
		t.Fatal("credential rejection accepted")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
}
