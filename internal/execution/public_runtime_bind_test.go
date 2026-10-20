package execution_test

import (
	context "context"
	sql "database/sql"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestBindPreservesNativeSQL(t *testing.T) {
	for _, tc := range []struct {
		mode   oceanbase.Mode
		status byte
		query  string
	}{
		{oceanbase.ModeMySQL, 2, "CREATE PROCEDURE p() BEGIN SELECT 1; END"},
		{oceanbase.ModeOracle, 6, "CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;"},
		{oceanbase.ModeOracle, 6, "DECLARE PROCEDURE p IS BEGIN NULL; END; BEGIN p; END;"},
		{oceanbase.ModeMySQL, 2, "SELECT /*! SQL_NO_CACHE */ 1"},
		{oceanbase.ModeMySQL, 2, `SELECT ROW_COUNT(), '"\a'`},
	} {
		t.Run(tc.query, func(t *testing.T) {
			connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: tc.mode, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, tc.status) || !testpeer.ServeBaseline(ctx, t, p, tc.status) {
					return
				}
				ctx = context.Background()
				if !testpeer.ReadQuery(ctx, t, p, tc.query) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, tc.status, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			if _, err = db.ExecContext(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBindANSIQuotesSession(t *testing.T) {
	query := "SELECT 1 AS \"a\\\""
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, query) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("a\\", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), query).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestBindRequestRejectsBeforeSend(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{selectBindSQL, "SELECT TRUE"} {
		if _, err := db.ExecContext(context.Background(), query, sql.Named("unexpected", 1)); err == nil {
			t.Fatal("invalid command sent")
		}
	}
}
