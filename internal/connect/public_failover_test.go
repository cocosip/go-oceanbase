package connect_test

import (
	"context"
	"database/sql/driver"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestRoleTransactionSocket(t *testing.T) {
	var dials atomic.Int32
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Routing: config.RoutingOptions{Role: config.RoleReadOnly}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		dials.Add(1)
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ ONLY"), 3) || !testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
