package execution_test

import (
	context "context"
	sql "database/sql"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestClientPrepareSessionChanges(t *testing.T) {
	c, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, PrepareMode: oceanbase.PrepareClient, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !testpeer.ReadQuery(ctx, t, p, "SELECT  _utf8mb4 X'615c62'  /* ? */") || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 2, 0, 0}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 'a\\',  1 "), 2) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT  _utf8mb4 X'615c62'  /* ? */"), 2) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT '{fn now()}'"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), "SELECT ? /* ? */")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = s.ExecContext(context.Background(), `a\b`); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err = db.ExecContext(context.Background(), "SELECT 'a\\', ?", 1); err != nil {
				t.Fatal("current SQL mode ignored", err)
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), "SELECT '{fn now()}'"); err != nil {
		t.Fatal(err)
	}
}
