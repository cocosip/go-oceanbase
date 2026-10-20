package connect_test

import (
	"context"
	"database/sql"
	"net"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestFailoverNoBusinessReplay(t *testing.T) {
	var dials, commands atomic.Int32
	dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		if testpeer.ReadQuery(context.Background(), t, p, "UPDATE once") {
			commands.Add(1)
		}
	})
	connector, err := oceanbase.NewConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, MaxAttempts: 4}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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
