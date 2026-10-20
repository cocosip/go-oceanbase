package oceanbase

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestSensitiveCursorQuery(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"SELECT id,v FROM t WHERE id=:id ORDER BY id", "SELECT id,v, ROWID FROM t WHERE id=:id ORDER BY id"},
		{"SELECT * FROM s.t x WHERE id=?", "SELECT x.*, ROWID FROM s.t x WHERE id=?"},
		{`SELECT * FROM "S"."T"`, `SELECT "T".*, ROWID FROM "S"."T"`},
		{"/* lead */ SELECT t.* FROM t", "/* lead */ SELECT t.*, ROWID FROM t"},
		{"SELECT /*+ FULL(t) */ id,v FROM t ORDER BY 2 DESC,1", "SELECT /*+ FULL(t) */ id,v, ROWID FROM t ORDER BY 2 DESC,1"},
	} {
		got, err := sensitiveCursorQuery(tc.query)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q, %v", tc.query, got, err)
		}
	}
	for _, query := range []string{"SELECT DISTINCT id FROM t", "SELECT COUNT(*) FROM t", "SELECT t.id FROM t JOIN u ON t.id=u.id", "SELECT id FROM t; DELETE FROM t"} {
		if _, err := sensitiveCursorQuery(query); !errors.Is(err, ErrCursor) {
			t.Fatalf("accepted non-refreshable query %q: %v", query, err)
		}
	}
}

func TestSensitiveCursorRefreshTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		t.Run(variant, func(t *testing.T) {
			const native = "SELECT id, ROWID FROM cursor_fixture"
			const refresh = `SELECT "id" FROM "s"."t" WHERE ROWID = ?`
			db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !servePrepareStatus(ctx, t, p, native, 54, 0, nil, 5) {
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
				if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, combinedHeader(54, 2, 0, 0), cursorBaseColumn("id", 8), textColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 69, 0}, row, []byte{254, 0, 0, 69, 0}, []byte{0, 1, 0, 5, 0, 0, 0}) {
					return
				}
				fetch := append(cursorFetchFixture(54, 1), 32, 0, 1, 0, 0, 0, 1, 0, 0, 0)
				if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), textColumn("ROWID", 253, 63, 0), []byte{254, 0, 0, 69, 0}, row, []byte{254, 0, 0, 133, 0}, []byte{0, 1, 0, 5, 0, 0, 0}) {
					return
				}
				if !servePrepareStatus(ctx, t, p, refresh, 55, 1, nil, 5) || !readCommand(ctx, t, p, append(executeHeader(55), 0, 1, 253, 0, 4, 'A', 'A', 'A', 'A')) || !sendPackets(ctx, t, p, []byte{1}, cursorBaseColumn("id", 210), []byte{254, 0, 0, 5, 0}, complexLenBytes([]byte{0, 0}, lobOutFixture(3)), []byte{254, 0, 0, 4, 0}) {
					return
				}
				// The complete response invalidates the cursor before any stale locator read.
				if !readCommand(ctx, t, p, stmtPacket(25, 55)) || !readCommand(ctx, t, p, stmtPacket(25, 54)) || !serveOK(ctx, t, p, []byte("\x03SELECT 1"), 4) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
			cur, err := OpenCursor(ctx, tx, "SELECT id FROM cursor_fixture", CursorOptions{Mode: CursorSensitive})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cur.Close() }()
			if rows, err := cur.Fetch(ctx, CursorPosition{CursorAbsolute, 1}, 1); !errors.Is(err, ErrResource) {
				if rows != nil {
					_ = rows.Close()
				}
				t.Fatal("refresh accepted ended transaction", err)
			}
			if _, err = cur.Fetch(ctx, CursorPosition{}, 1); !errors.Is(err, ErrResource) {
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
