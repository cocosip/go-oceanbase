package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testCursorEpochPlain = "plain"

func TestCursorColumnTransactionInvalidation(t *testing.T) {
	const lobKind, childKind = "lob", "child"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, kind := range []string{"scalar-more", "scalar-last", lobKind, childKind} {
			t.Run(variant+"/"+kind, func(t *testing.T) {
				db := batchRewriteDB(t, Config{Mode: ModeOracle, PrepareMode: PrepareServer, Limits: Limits{FetchSize: 1}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) ||
						!servePrepareStatus(ctx, t, p, "SELECT child FROM t", 91, 0, nil, 5) ||
						!readCommand(ctx, t, p, executeHeader(91)) ||
						!sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}) {
						return
					}
					typ := byte(8)
					row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
					status := byte(132)
					switch kind {
					case "scalar-more":
						status = 4
					case lobKind:
						typ, row = 210, complexLenBytes([]byte{0, 0}, lobOutFixture(3))
					case childKind:
						typ, row = 163, binary.LittleEndian.AppendUint32([]byte{0, 0}, 93)
					}
					if !readCommand(ctx, t, p, cursorFetchFixture(92, 1)) ||
						!sendPackets(ctx, t, p, []byte{1}, textColumn("v", typ, 63, 0), []byte{254, 0, 0, 5, 0}, row, []byte{254, 0, 0, status, 0}) {
						return
					}
					// Only cleanup may follow; neither another fetch nor LOB/child materialization is valid.
					if !readCommand(ctx, t, p, stmtPacket(25, 92)) {
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
				if !errors.Is(err, ErrResource) || rows != nil {
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

func TestCursorFetchTransactionInvalidation(t *testing.T) {
	const query = "SELECT payload FROM t"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, mode := range []CursorMode{CursorForward, CursorStatic} {
			t.Run(variant+"/"+map[CursorMode]string{CursorForward: "forward", CursorStatic: "static"}[mode], func(t *testing.T) {
				db := batchRewriteDB(t, Config{Mode: ModeOracle, Limits: Limits{FetchSize: 1}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) ||
						!servePrepareStatus(ctx, t, p, query, 54, 0, nil, 5) ||
						!readCommand(ctx, t, p, cursorExecuteFixture(54)) ||
						!sendPackets(ctx, t, p, []byte{1}, textColumn("payload", 210, 63, 0), []byte{254, 0, 0, 69, 0}) {
						return
					}
					fetch := append(cursorFetchFixture(54, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
					if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p, []byte{1}, textColumn("payload", 210, 63, 0), []byte{254, 0, 0, 5, 0},
						complexLenBytes([]byte{0, 0}, lobOutFixture(3)), []byte{254, 0, 0, 132, 0}, []byte{0, 1, 0, 4, 0, 0, 0}) ||
						!readCommand(ctx, t, p, stmtPacket(25, 54)) ||
						!serveTxSteps(ctx, t, p, []txStep{{"SELECT 1", 4}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
				cur, err := OpenCursor(ctx, tx, query, CursorOptions{Mode: mode})
				if mode == CursorForward {
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = cur.Close() }()
					var rows *sql.Rows
					rows, err = cur.Fetch(ctx, CursorPosition{}, 1)
					if rows != nil {
						_ = rows.Close()
						t.Fatal("stale fetch published a batch")
					}
				} else if cur != nil {
					_ = cur.Close()
					t.Fatal("stale static cache published a cursor")
				}
				if !errors.Is(err, ErrResource) {
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
