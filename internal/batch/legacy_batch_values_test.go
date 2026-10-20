package batch_test

import (
	"context"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"strings"
	"testing"
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
	for _, variant := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		for _, outcome := range []string{"ok", testValuesServer, testValuesDisconnect} {
			t.Run(fmt.Sprint(variant, "/", outcome), func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, oceanbase.Config{}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") {
						return
					}
					if outcome == testValuesDisconnect {
						return
					}
					response := publictest.CommandPacket(2, 17, 2)
					if outcome == testValuesServer {
						response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if !testpeer.SendPackets(ctx, t, p, response) {
						return
					}
					if outcome == "ok" && (!testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 3 )") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 31, 2))) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkRows: 2})
				if r.Strategy != oceanbase.BatchMultiValues || len(r.Chunks) == 0 {
					t.Fatalf("%+v %v", r, err)
				}
				if outcome == "ok" {
					if err != nil || len(r.Chunks) != 2 || r.Chunks[0].Start != 0 || r.Chunks[0].End != 2 || r.Chunks[0].RowsAffected != 2 || r.Chunks[0].InsertID != "17" || !r.Chunks[0].InsertIDKnown || r.Items[0].RowsAffectedKnown || r.Items[0].InsertIDKnown || r.Items[2].InsertID != "31" {
						t.Fatalf("%+v %v", r, err)
					}
				} else {
					status := oceanbase.BatchFailed
					if outcome == testValuesDisconnect {
						status = oceanbase.BatchUnknown
					}
					if err == nil || r.Items[0].Status != status || r.Items[1].Status != status || r.Items[2].Status != oceanbase.BatchNotExecuted {
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
			cfg := oceanbase.Config{}
			rows := [][]any{{1}, {2}, {3}, {4}}
			opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkParameters: 2}
			if scenario == testValuesBytes {
				cfg.Limits.MaxMessageBytes = 192
				rows = [][]any{{strings.Repeat("a", 50)}, {strings.Repeat("b", 50)}}
			}
			if scenario == testValuesInvalid {
				rows[2] = []any{3, 4}
			}
			if scenario == testValuesContinue {
				opts.ContinueOnError = true
			}
			db := publictest.BatchRewriteDB(t, cfg, testpeer.Standard, func(peerCtx context.Context, p *wire.PacketIO) {
				first := "INSERT INTO t VALUES ( 1 ),( 2 )"
				if scenario == testValuesBytes {
					first = "INSERT INTO t VALUES ( _utf8mb4 X'" + strings.Repeat("61", 50) + "' )"
				}
				if !testpeer.ReadQuery(peerCtx, t, p, first) {
					return
				}
				if scenario == testValuesCancel {
					cancel()
					_, _ = p.ReadMessage(peerCtx)
					return
				}
				if scenario == testValuesMalformed {
					_ = testpeer.SendPackets(peerCtx, t, p, publictest.CommandPacket(2, 17, 10))
					return
				}
				response := publictest.CommandPacket(2, 17, 2)
				if scenario == testValuesContinue {
					response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
				}
				if !testpeer.SendPackets(peerCtx, t, p, response) {
					return
				}
				if scenario != testValuesInvalid {
					second := "INSERT INTO t VALUES ( 3 ),( 4 )"
					if scenario == testValuesBytes {
						second = "INSERT INTO t VALUES ( _utf8mb4 X'" + strings.Repeat("62", 50) + "' )"
					}
					if !testpeer.ReadQuery(peerCtx, t, p, second) || !testpeer.SendPackets(peerCtx, t, p, publictest.CommandPacket(2, 31, 2)) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(ctx, db, testBatchSQL, rows, opts)
			checkBatchValuesOutcome(t, scenario, r, err, len(rows))
		})
	}
}

func TestBatchValuesFallback(t *testing.T) {
	const query = "INSERT IGNORE INTO t VALUES (?)"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{PrepareMode: oceanbase.PrepareClient}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{"INSERT IGNORE INTO t VALUES ( 1 )", "INSERT IGNORE INTO t VALUES ( 2 )"} {
			if !testpeer.ReadQuery(ctx, t, p, q) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues})
	if err != nil || r.Strategy != oceanbase.BatchSequential || len(r.Chunks) != 0 || batchSucceeded(&r) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestBatchValuesTransaction(t *testing.T) {
	db := publictest.TxDB(t, oceanbase.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 ),( 2 )") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(2, 17, 3)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := oceanbase.ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues})
	if err != nil || r.Strategy != oceanbase.BatchMultiValues || batchSucceeded(&r) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func checkBatchValuesOutcome(t *testing.T, scenario string, r oceanbase.BatchResult, err error, count int) {
	t.Helper()
	switch scenario {
	case testValuesInvalid:
		if !errors.Is(err, oceanbase.ErrBind) || batchSucceeded(&r) != 2 || r.Items[2].Started || r.Items[3].Status != oceanbase.BatchNotExecuted {
			t.Fatalf("%+v %v", r, err)
		}
	case testValuesContinue:
		if err == nil || batchSucceeded(&r) != 2 || r.Items[0].Status != oceanbase.BatchFailed || r.Items[2].Status != oceanbase.BatchSucceeded {
			t.Fatalf("%+v %v", r, err)
		}
	case testValuesCancel, testValuesMalformed:
		if err == nil || r.Items[0].Status != oceanbase.BatchUnknown || r.Items[2].Started {
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
