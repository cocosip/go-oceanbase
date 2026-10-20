package materialize_test

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

func TestCursorColumnTransactionInvalidation(t *testing.T) {
	const lobKind, childKind = "lob", "child"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, kind := range []string{"scalar-more", "scalar-last", lobKind, childKind} {
			t.Run(variant+"/"+kind, func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle, PrepareMode: config.PrepareServer, Limits: config.Limits{FetchSize: 1}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) ||
						!publictest.ServePrepareStatus(ctx, t, p, "SELECT child FROM t", 91, 0, nil, 5) ||
						!testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) ||
						!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}) {
						return
					}
					typ := byte(8)
					row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
					status := byte(132)
					switch kind {
					case "scalar-more":
						status = 4
					case lobKind:
						typ, row = 210, wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobOutFixture(3))
					case childKind:
						typ, row = 163, binary.LittleEndian.AppendUint32([]byte{0, 0}, 93)
					}
					if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 1)) ||
						!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", typ, 63, 0), []byte{254, 0, 0, 5, 0}, row, []byte{254, 0, 0, status, 0}) {
						return
					}
					// Only cleanup may follow; neither another fetch nor LOB/child materialization is valid.
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) {
						return
					}
					p.ResetSequence()
					if command, err := p.ReadMessage(ctx); err == nil {
						t.Errorf("invalid child materialization retained session: command=%x", command[:min(16, len(command))])
					}
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				stmt, err := tx.PrepareContext(ctx, "SELECT child FROM t")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stmt.Close() }()
				rows, err := stmt.QueryContext(ctx)
				if rows != nil {
					_ = rows.Close()
				}
				if !errors.Is(err, oceanbase.ErrResource) || rows != nil {
					t.Fatalf("invalid transaction published child rows: rows=%v err=%v", rows, err)
				}
				_ = tx.Rollback()
				if db.Stats().Idle != 0 {
					t.Fatal("failed materialization pooled its connection")
				}
			})
		}
	}
}
