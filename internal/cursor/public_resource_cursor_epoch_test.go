package cursor_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorFetchTransactionInvalidation(t *testing.T) {
	const query = "SELECT payload FROM t"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, mode := range []oceanbase.CursorMode{oceanbase.CursorForward, oceanbase.CursorStatic} {
			t.Run(variant+"/"+map[oceanbase.CursorMode]string{oceanbase.CursorForward: "forward", oceanbase.CursorStatic: "static"}[mode], func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle, Limits: config.Limits{FetchSize: 1}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) ||
						!publictest.ServePrepareStatus(ctx, t, p, query, 54, 0, nil, 5) ||
						!testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(54)) ||
						!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("payload", 210, 63, 0), []byte{254, 0, 0, 69, 0}) {
						return
					}
					fetch := append(publictest.CursorFetchFixture(54, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
					if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("payload", 210, 63, 0), []byte{254, 0, 0, 5, 0},
						wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobOutFixture(3)), []byte{254, 0, 0, 132, 0}, []byte{0, 1, 0, 4, 0, 0, 0}) ||
						!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 54)) ||
						!publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: "SELECT 1", Status: 4}, {Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				cur, err := oceanbase.OpenCursor(ctx, tx, query, oceanbase.CursorOptions{Mode: mode})
				if mode == oceanbase.CursorForward {
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = cur.Close() }()
					var rows *sql.Rows
					rows, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
					if rows != nil {
						_ = rows.Close()
						t.Fatal("stale fetch published a batch")
					}
				} else if cur != nil {
					_ = cur.Close()
					t.Fatal("stale static cache published a cursor")
				}
				if !errors.Is(err, oceanbase.ErrResource) {
					t.Fatal("lost transaction invalidation", err)
				}
				if _, err = tx.ExecContext(ctx, "SELECT 1"); err != nil {
					t.Fatal("complete fetch damaged the session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
