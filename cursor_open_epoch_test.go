package oceanbase

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorOpenTransactionInvalidation(t *testing.T) {
	const query = "SELECT payload FROM cursor_open_epoch"
	const prepareStage = "prepare"
	const tailStage = "tail"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			for _, cursorMode := range []CursorMode{CursorForward, CursorStatic, CursorSensitive} {
				if mode == ModeMySQL && cursorMode == CursorSensitive {
					continue
				}
				for _, stage := range []string{prepareStage, "execute", tailStage} {
					if stage == tailStage && cursorMode != CursorSensitive {
						continue
					}
					name := map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode] + "/" + variant + "/" + map[CursorMode]string{CursorForward: "forward", CursorStatic: "static", CursorSensitive: "sensitive"}[cursorMode] + "/" + stage
					t.Run(name, func(t *testing.T) {
						active, ended := byte(3), byte(2)
						begin := []txStep{{testMySQLReadWrite, 3}}
						finish := []txStep{{testRollbackSQL, 2}}
						if mode == ModeOracle {
							active, ended = 5, 4
							begin = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
							finish = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
						}
						db := batchRewriteDB(t, Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
							prepareStatus := active
							if stage == prepareStage {
								prepareStatus = ended
								// No execute command may follow this metadata response.
							}
							if !serveTxSteps(ctx, t, p, begin) || !servePrepareStatus(ctx, t, p, query, 94, 0, [][]byte{textColumn("payload", 8, 63, 0)}, prepareStatus) {
								return
							}
							if stage != prepareStage {
								if cursorMode == CursorSensitive {
									payload, err := wire.EncodePrepareExecute(94, []byte(query), nil, 4096)
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
									if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, combinedHeader(94, 1, 0, 0), textColumn("payload", 210, 63, 0), []byte{254, 0, 0, active | 64, 0}, complexLenBytes([]byte{0, 0}, lobOutFixture(3)), []byte{254, 0, 0, rowStatus | 64, 0}, []byte{0, 1, 0, ended, 0, 0, 0}) {
										return
									}
								} else if !readCommand(ctx, t, p, cursorExecuteFixture(94)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("payload", 8, 63, 0), []byte{254, 0, 0, ended | 64, 0}) {
									return
								}
							}
							if !readCommand(ctx, t, p, stmtPacket(25, 94)) || !serveOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !serveTxSteps(ctx, t, p, finish) {
								return
							}
							p.ResetSequence()
							serveQuit(t, p)
						})
						defer func() { _ = db.Close() }()
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						tx, err := db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = tx.Rollback() }()
						cur, err := OpenCursor(ctx, tx, query, CursorOptions{Mode: cursorMode})
						if cur != nil {
							_ = cur.Close()
							t.Fatal("opening response published a stale cursor")
						}
						if !errors.Is(err, ErrResource) {
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
