package oceanbase

import (
	context "context"
	sql "database/sql"
	testing "testing"

	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func queryDB(t *testing.T, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

var sendPackets = testpeer.SendPackets
