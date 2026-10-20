package connect_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDiscoveryLifecycle(t *testing.T) {
	var lookups atomic.Int32
	var dialed string
	var lookupContext context.Context
	source := config.TopologySource(func(ctx context.Context) (config.Topology, error) {
		lookups.Add(1)
		lookupContext = ctx
		return config.Topology{Service: discoveryTestService, Endpoints: []config.Endpoint{{Addr: discoveryTestHost, Role: config.RoleReadWrite}, {Addr: "other", Role: config.RoleReadOnly}}}, nil
	})
	dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		// The discovery context is canceled before these commands, without closing this socket.
		if !testpeer.ServeOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	c, err := oceanbase.NewConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Role: config.RoleReadWrite}, Discovery: config.DiscoveryOptions{Service: discoveryTestService, Source: source}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { dialed = a; return dial(ctx, n, a) }})
	if err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 0 {
		t.Fatal("constructor performed lookup")
	}
	physical, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dialed != discoveryTestAddr {
		t.Fatal(dialed)
	}
	if err = c.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if lookupContext.Err() == nil {
		t.Fatal("lookup context not canceled")
	}
	if err = physical.(driver.Pinger).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = physical.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Connect(context.Background()); !errors.Is(err, dberrors.ErrConnectorClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestDriverOpenClosesPrivateDiscovery(t *testing.T) {
	requests := make(chan struct{}, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requests <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	q := url.Values{"ocpURL": {server.URL}, "discoveryService": {discoveryTestService}, "discoveryRefreshInterval": {"2ms"}, "discoveryTimeout": {"100ms"}, "discoveryMaxAge": {"10ms"}}
	if physical, err := driverInstance().Open("oceanbase://user@localhost/?" + q.Encode()); err == nil {
		_ = physical.Close()
		t.Fatal("Open unexpectedly succeeded")
	}
	// A request already sent before cancellation can reach the server after Close returns.
	// Observe quiescence instead of treating that in-flight request as a new refresh.
	quiet := time.NewTimer(30 * time.Millisecond)
	defer quiet.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-requests:
			quiet.Reset(30 * time.Millisecond)
		case <-quiet.C:
			return
		case <-deadline.C:
			t.Fatal("private Connector kept refreshing after Open failure")
		}
	}
}
