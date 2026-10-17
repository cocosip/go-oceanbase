package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const routeFirst = "first.example:2881"
const routeSecond = "second.example:2881"

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
	connector, err := NewConnector(Config{User: testUser, ConnectTimeout: time.Second, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(addresses, []string{routeFirst, routeSecond}) || deadlines[1].Before(deadlines[0]) {
		t.Fatal(addresses, deadlines)
	}
}

func TestRoleIsolation(t *testing.T) {
	endpoints := []Endpoint{{Addr: routeFirst, Role: RoleReadWrite}, {Addr: routeSecond, Role: RoleReadOnly}}
	for _, role := range []EndpointRole{RoleReadWrite, RoleReadOnly} {
		var addresses []string
		connector, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Endpoints: endpoints, Role: role}, DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			addresses = append(addresses, addr)
			return nil, errors.New("dial failure")
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = connector.Connect(context.Background())
		want := routeFirst
		if role == RoleReadOnly {
			want = routeSecond
		}
		if !reflect.DeepEqual(addresses, []string{want}) {
			t.Fatal(role, addresses)
		}
	}
	if _, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Role: RoleReadOnly, Endpoints: []Endpoint{{Addr: routeFirst, Role: RoleReadWrite}}}}); err == nil {
		t.Fatal("missing role accepted")
	}
}

func TestFailoverNoBusinessReplay(t *testing.T) {
	var dials, commands atomic.Int32
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		if readQuery(context.Background(), t, p, "UPDATE once") {
			commands.Add(1)
		}
	})
	connector, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 4}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx, network, addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(context.Background(), "UPDATE once"); err == nil {
		t.Fatal("missing response accepted")
	}
	if dials.Load() != 1 || commands.Load() != 1 {
		t.Fatal(dials.Load(), commands.Load())
	}
}
