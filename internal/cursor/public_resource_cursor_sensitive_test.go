package cursor_test

import (
	context "context"
	binary "encoding/binary"
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

func TestSensitiveCursorRefreshTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		t.Run(variant, func(t *testing.T) {
			const native = "SELECT id, ROWID FROM cursor_fixture"
			const refresh = `SELECT "id" FROM "s"."t" WHERE ROWID = ?`
			db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServePrepareStatus(ctx, t, p, native, 54, 0, nil, 5) {
					return
				}
				payload, err := wire.EncodePrepareExecute(54, []byte(native), nil, 4096)
				if err != nil {
					t.Error(err)
					return
				}
				payload[5] = 1
				binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
				row := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 1), 4, 'A', 'A', 'A', 'A')
				if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(54, 2, 0, 0), cursorBaseColumn("id", 8), testpeer.TextColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 69, 0}, row, []byte{254, 0, 0, 69, 0}, []byte{0, 1, 0, 5, 0, 0, 0}) {
					return
				}
				fetch := append(publictest.CursorFetchFixture(54, 1), 32, 0, 1, 0, 0, 0, 1, 0, 0, 0)
				if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), testpeer.TextColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 69, 0}, row, []byte{254, 0, 0, 133, 0}, []byte{0, 1, 0, 5, 0, 0, 0}) {
					return
				}
				if !publictest.ServePrepareStatus(ctx, t, p, refresh, 55, 1, nil, 5) || !testpeer.ReadCommand(ctx, t, p, append(publictest.ExecuteHeader(55), 0, 1, 253, 0, 4, 'A', 'A', 'A', 'A')) || !testpeer.SendPackets(ctx, t, p, []byte{1}, cursorBaseColumn("id", 210), []byte{254, 0, 0, 5, 0}, wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobOutFixture(3)), []byte{254, 0, 0, 4, 0}) {
					return
				}
				// The complete response invalidates the cursor before any stale locator read.
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 55)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 54)) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 1"), 4) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
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
			cur, err := oceanbase.OpenCursor(ctx, tx, "SELECT id FROM cursor_fixture", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cur.Close() }()
			if rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1); !errors.Is(err, oceanbase.ErrResource) {
				if rows != nil {
					_ = rows.Close()
				}
				t.Fatal("refresh accepted ended transaction", err)
			}
			if _, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal("refresh retained invalid cursor", err)
			}
			if _, err = tx.ExecContext(ctx, "SELECT 1"); err != nil {
				t.Fatal("complete refresh damaged session", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
