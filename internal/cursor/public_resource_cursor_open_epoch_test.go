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

func TestCursorOpenTransactionInvalidation(t *testing.T) {
	const query = "SELECT payload FROM cursor_open_epoch"
	const prepareStage = "prepare"
	const tailStage = "tail"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			for _, cursorMode := range []oceanbase.CursorMode{oceanbase.CursorForward, oceanbase.CursorStatic, oceanbase.CursorSensitive} {
				if mode == config.ModeMySQL && cursorMode == oceanbase.CursorSensitive {
					continue
				}
				for _, stage := range []string{prepareStage, "execute", tailStage} {
					if stage == tailStage && cursorMode != oceanbase.CursorSensitive {
						continue
					}
					name := map[config.Mode]string{config.ModeMySQL: publictest.TransactionMySQLLabel, config.ModeOracle: publictest.TransactionOracleLabel}[mode] + "/" + variant + "/" + map[oceanbase.CursorMode]string{oceanbase.CursorForward: "forward", oceanbase.CursorStatic: "static", oceanbase.CursorSensitive: "sensitive"}[cursorMode] + "/" + stage
					t.Run(name, func(t *testing.T) {
						active, ended := byte(3), byte(2)
						begin := []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}
						finish := []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}
						if mode == config.ModeOracle {
							active, ended = 5, 4
							begin = []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}
							finish = []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}
						}
						db := publictest.BatchRewriteDB(t, config.Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
							native := query
							if cursorMode == oceanbase.CursorSensitive {
								native = "SELECT payload, ROWID FROM cursor_open_epoch"
							}
							prepareStatus := active
							if stage == prepareStage {
								prepareStatus = ended
								// No execute command may follow this metadata response.
							}
							if !publictest.ServeTxSteps(ctx, t, p, begin) || !publictest.ServePrepareStatus(ctx, t, p, native, 94, 0, [][]byte{testpeer.TextColumn("payload", 8, 63, 0)}, prepareStatus) {
								return
							}
							if stage != prepareStage {
								if cursorMode == oceanbase.CursorSensitive {
									payload, err := wire.EncodePrepareExecute(94, []byte(native), nil, 4096)
									if err != nil {
										t.Error(err)
										return
									}
									payload[5] = 1
									binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
									rowStatus := ended
									if stage == tailStage {
										rowStatus = active
									}
									// A stale LOB must not trigger a DBMS_LOB read before rejection.
									if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(94, 1, 0, 0), testpeer.TextColumn("payload", 210, 63, 0), []byte{254, 0, 0, active | 64, 0}, wire.AppendComplexLenBytes([]byte{0, 0}, publictest.LobOutFixture(3)), []byte{254, 0, 0, rowStatus | 64, 0}, []byte{0, 1, 0, ended, 0, 0, 0}) {
										return
									}
								} else if !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(94)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("payload", 8, 63, 0), []byte{254, 0, 0, ended | 64, 0}) {
									return
								}
							}
							if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 94)) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !publictest.ServeTxSteps(ctx, t, p, finish) {
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
						cur, err := oceanbase.OpenCursor(ctx, tx, query, oceanbase.CursorOptions{Mode: cursorMode})
						if cur != nil {
							_ = cur.Close()
							t.Fatal("opening response published a stale cursor")
						}
						if !errors.Is(err, oceanbase.ErrResource) {
							t.Fatal("lost opening transaction invalidation", err)
						}
						if _, err = tx.ExecContext(ctx, "SELECT 1"); err != nil {
							t.Fatal("complete opening response damaged the session", err)
						}
						if err = tx.Rollback(); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		}
	}
}
