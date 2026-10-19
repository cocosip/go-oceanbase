package oceanbase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testValuesDisconnect = "values-disconnect"
	testValuesBytes      = "values-byte-budget"
	testValuesContinue   = "values-continue"
	testValuesCancel     = "values-cancel"
	testValuesMalformed  = "values-malformed"
	testValuesInvalid    = "values-invalid-row"
	testValuesServer     = "values-server-error"
)

func TestBatchMultiValues(t *testing.T) {
	for _, variant := range []string{testStandard, testCompression, testOB20} {
		for _, outcome := range []string{"ok", testValuesServer, testValuesDisconnect} {
			t.Run(fmt.Sprint(variant, "/", outcome), func(t *testing.T) {
				db := batchRewriteDB(t, Config{}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !readQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") {
						return
					}
					if outcome == testValuesDisconnect {
						return
					}
					response := commandPacket(2, 17, 2)
					if outcome == testValuesServer {
						response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if !sendPackets(ctx, t, p, response) {
						return
					}
					if outcome == "ok" && (!readQuery(ctx, t, p, "INSERT INTO t VALUES ( 3 )") || !sendPackets(ctx, t, p, commandPacket(1, 31, 2))) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchMultiValues, ChunkRows: 2})
				if r.Strategy != BatchMultiValues || len(r.Chunks) == 0 {
					t.Fatalf("%+v %v", r, err)
				}
				if outcome == "ok" {
					if err != nil || len(r.Chunks) != 2 || r.Chunks[0].Start != 0 || r.Chunks[0].End != 2 || r.Chunks[0].RowsAffected != 2 || r.Chunks[0].InsertID != "17" || !r.Chunks[0].InsertIDKnown || r.Items[0].RowsAffectedKnown || r.Items[0].InsertIDKnown || r.Items[2].InsertID != "31" {
						t.Fatalf("%+v %v", r, err)
					}
				} else {
					status := BatchFailed
					if outcome == testValuesDisconnect {
						status = BatchUnknown
					}
					if err == nil || r.Items[0].Status != status || r.Items[1].Status != status || r.Items[2].Status != BatchNotExecuted {
						t.Fatalf("%+v %v", r, err)
					}
				}
			})
		}
	}
}

func TestBatchValuesBudgetsAndErrors(t *testing.T) {
	for _, scenario := range []string{"values-parameter-budget", testValuesBytes, testValuesInvalid, testValuesContinue, testValuesCancel, testValuesMalformed} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := Config{}
			rows := [][]any{{1}, {2}, {3}, {4}}
			opts := BatchOptions{Strategy: BatchMultiValues, ChunkParameters: 2}
			if scenario == testValuesBytes {
				cfg.Limits.MaxMessageBytes = 128
				rows = [][]any{{strings.Repeat("a", 30)}, {strings.Repeat("b", 30)}}
			}
			if scenario == testValuesInvalid {
				rows[2] = []any{3, 4}
			}
			if scenario == testValuesContinue {
				opts.ContinueOnError = true
			}
			db := batchRewriteDB(t, cfg, testStandard, func(peerCtx context.Context, p *wire.PacketIO) {
				first := "INSERT INTO t VALUES ( 1 ),( 2 )"
				if scenario == testValuesBytes {
					first = "INSERT INTO t VALUES ( _utf8mb4 X'" + strings.Repeat("61", 30) + "' )"
				}
				if !readQuery(peerCtx, t, p, first) {
					return
				}
				if scenario == testValuesCancel {
					cancel()
					_, _ = p.ReadMessage(peerCtx)
					return
				}
				if scenario == testValuesMalformed {
					_ = sendPackets(peerCtx, t, p, commandPacket(2, 17, 10))
					return
				}
				response := commandPacket(2, 17, 2)
				if scenario == testValuesContinue {
					response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
				}
				if !sendPackets(peerCtx, t, p, response) {
					return
				}
				if scenario != testValuesInvalid {
					second := "INSERT INTO t VALUES ( 3 ),( 4 )"
					if scenario == testValuesBytes {
						second = "INSERT INTO t VALUES ( _utf8mb4 X'" + strings.Repeat("62", 30) + "' )"
					}
					if !readQuery(peerCtx, t, p, second) || !sendPackets(peerCtx, t, p, commandPacket(2, 31, 2)) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(ctx, db, testBatchSQL, rows, opts)
			checkBatchValuesOutcome(t, scenario, r, err, len(rows))
		})
	}
}

func TestBatchValuesFallback(t *testing.T) {
	const query = "INSERT IGNORE INTO t VALUES (?)"
	db := batchRewriteDB(t, Config{PrepareMode: PrepareClient}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{"INSERT IGNORE INTO t VALUES ( 1 )", "INSERT IGNORE INTO t VALUES ( 2 )"} {
			if !readQuery(ctx, t, p, q) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchMultiValues})
	if err != nil || r.Strategy != BatchSequential || len(r.Chunks) != 0 || batchSucceeded(&r) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestBatchValuesTransaction(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !readQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") || !sendPackets(ctx, t, p, commandPacket(2, 17, 3)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchMultiValues})
	if err != nil || r.Strategy != BatchMultiValues || batchSucceeded(&r) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchValuesShape(t *testing.T) {
	for _, q := range []string{testBatchSQL, "INSERT INTO `db`.`t` (`a`, b) VALUES (?,?)"} {
		if _, _, ok := batchValuesPlan(q, false); !ok {
			t.Fatal(q)
		}
	}
	for _, q := range []string{"INSERT IGNORE INTO t VALUES (?)", "INSERT INTO t VALUES (?),(?)", "INSERT INTO t VALUES (?) ON DUPLICATE KEY UPDATE a=1", "INSERT INTO t SELECT ?", "INSERT INTO t VALUES (f(?))", "INSERT INTO t VALUES (?); DELETE FROM t", "INSERT INTO t VALUES (?) RETURNING a"} {
		if _, _, ok := batchValuesPlan(q, false); ok {
			t.Fatal(q)
		}
	}
}

func checkBatchValuesOutcome(t *testing.T, scenario string, r BatchResult, err error, count int) {
	t.Helper()
	switch scenario {
	case testValuesInvalid:
		if !errors.Is(err, ErrBind) || batchSucceeded(&r) != 2 || r.Items[2].Started || r.Items[3].Status != BatchNotExecuted {
			t.Fatalf("%+v %v", r, err)
		}
	case testValuesContinue:
		if err == nil || batchSucceeded(&r) != 2 || r.Items[0].Status != BatchFailed || r.Items[2].Status != BatchSucceeded {
			t.Fatalf("%+v %v", r, err)
		}
	case testValuesCancel, testValuesMalformed:
		if err == nil || r.Items[0].Status != BatchUnknown || r.Items[2].Started {
			t.Fatalf("%+v %v", r, err)
		}
		if scenario == testValuesCancel && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	default:
		if err != nil || len(r.Chunks) != 2 || batchSucceeded(&r) != count {
			t.Fatalf("%+v %v", r, err)
		}
	}
}
