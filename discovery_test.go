package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const discoveryTestHost = "first"
const discoveryTestAddr = "first:2881"

const discoveryTestService = "orders"

const discoveryTestFile = "tnsnames.ob"
const discoveryTestAlias = "orders="

const discoveryDescriptor = `(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(HOST=first)(TYPE=master)))(CONNECT_DATA=(SERVICE_NAME=orders)))`

func TestDiscoveryLifecycle(t *testing.T) {
	var lookups atomic.Int32
	var dialed string
	var lookupContext context.Context
	source := TopologySource(func(ctx context.Context) (Topology, error) {
		lookups.Add(1)
		lookupContext = ctx
		return Topology{Service: discoveryTestService, Endpoints: []Endpoint{{Addr: discoveryTestHost, Role: RoleReadWrite}, {Addr: "other", Role: RoleReadOnly}}}, nil
	})
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		// The discovery context is canceled before these commands, without closing this socket.
		if !serveOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Role: RoleReadWrite}, Discovery: DiscoveryOptions{Service: discoveryTestService, Source: source}, DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { dialed = a; return dial(ctx, n, a) }})
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
	if _, err = c.Connect(context.Background()); !errors.Is(err, ErrConnectorClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestDiscoveryConnectBudget(t *testing.T) {
	c, err := NewConnector(Config{User: testUser, ConnectTimeout: 20 * time.Millisecond, Discovery: DiscoveryOptions{Service: discoveryTestService, RequestTimeout: time.Second, Source: func(ctx context.Context) (Topology, error) { <-ctx.Done(); return Topology{}, ctx.Err() }}})
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
	if err = c.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverySources(t *testing.T) {
	path := filepath.Join(t.TempDir(), discoveryTestFile)
	if err := os.WriteFile(path, []byte(discoveryTestAlias+discoveryDescriptor), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewTNSFileSource(path, discoveryTestService)
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
	source, err = NewOCPSource(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if topology, err = source(context.Background()); err != nil || topology.Endpoints[0].Addr != discoveryTestAddr {
		t.Fatalf("OCP: %+v %v", topology, err)
	}
	source, err = NewOCPSource(server.URL+"/redirect", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestDiscoveryDSN(t *testing.T) {
	query := url.Values{"tnsFile": {discoveryTestFile}, "tnsAlias": {discoveryTestService}, "discoveryService": {discoveryTestService}, "discoveryRefreshInterval": {"1s"}, "discoveryTimeout": {"2s"}, "discoveryMaxAge": {"5s"}}
	cfg, err := ParseDSN("oceanbase://user@localhost/?" + query.Encode())
	if err != nil || cfg.Discovery.Source == nil || cfg.Discovery.RefreshInterval != time.Second || cfg.Routing.MaxAttempts != 0 || cfg.Routing.AttemptTimeout != 0 {
		t.Fatalf("DSN: %+v %v", cfg.Discovery, err)
	}
	for _, q := range []string{"tnsFile=x", "ocpURL=http://localhost", "discoveryService=orders", "tnsFile=x&tnsAlias=orders&ocpURL=http://localhost&discoveryService=orders", "ocpURL=http://user:secret@localhost&discoveryService=orders", "discoveryTimeout=-1s", "addressDescription=" + url.QueryEscape(discoveryDescriptor) + "&addresses=other"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?" + q); err == nil {
			t.Fatal("accepted ambiguous discovery config")
		}
	}
	cfg, err = ParseDSN("oceanbase://user@localhost/?addressDescription=" + url.QueryEscape(discoveryDescriptor))
	if err != nil || len(cfg.Routing.Endpoints) != 1 || cfg.Routing.Endpoints[0].Addr != discoveryTestAddr || cfg.Routing.Endpoints[0].Role != RoleReadWrite {
		t.Fatalf("descriptor: %+v %v", cfg.Routing, err)
	}
}

func TestTNSFileRequiresSelectedAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), discoveryTestFile)
	for _, content := range []string{discoveryDescriptor, "other=" + discoveryDescriptor, "other=" + discoveryDescriptor + "\n" + discoveryDescriptor} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := NewTNSFileSource(path, discoveryTestService)
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
	source, err := NewTNSFileSource(link, discoveryTestService)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := source(context.Background()); err != nil || s.Service != discoveryTestService {
		t.Fatalf("regular target: %+v %v", s, err)
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
	if physical, err := (drv{}).Open("oceanbase://user@localhost/?" + q.Encode()); err == nil {
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
