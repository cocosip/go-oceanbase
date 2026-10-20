package batch_test

import (
	"context"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"math"
	"testing"
)

const batchRewriteGuard = "SELECT MAX(COALESCE(LOWER(VALUE),'')!='false') FROM oceanbase.GV$OB_PARAMETERS WHERE NAME='ob_enable_batched_multi_statement'"
const batchRewritePair = "INSERT INTO t VALUES ( 1 )\n;\nINSERT INTO t VALUES ( 2 )"

func serveBatchRewriteGuard(ctx context.Context, t testing.TB, p *wire.PacketIO, value string, status byte) bool {
	t.Helper()
	return testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) && testpeer.SendPackets(ctx, t, p,
		[]byte{1}, testpeer.TextColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, status, 0},
		append([]byte{byte(len(value))}, value...), []byte{254, 0, 0, status, 0})
}

func TestBatchRewriteGuardServerError(t *testing.T) {
	for _, code := range []uint16{1064, 1142, 1193} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeMySQL, AllowMultiStatements: true}, "standard", func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) || !testpeer.SendPackets(ctx, t, p, []byte{255, byte(code), byte(code >> 8), '#', 'H', 'Y', '0', '0', '0', 'n', 'o'}) {
					return
				}
				testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, "INSERT INTO t VALUES (?)", [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
			var server *oceanbase.Error
			if !errors.As(err, &server) || server.Code != uint32(code) || r.Items[0].Started || r.Items[1].Started {
				t.Fatalf("guard error swallowed or DML sent: result=%+v err=%v", r, err)
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchRewriteResults(t *testing.T) {
	for _, variant := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprint(variant, "/", fail), func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair+"\n;\nINSERT INTO t VALUES ( 3 )") {
						return
					}
					second := publictest.CommandPacket(2, 0, 10)
					if fail {
						second = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, math.MaxUint64, 10), second) {
						return
					}
					if !fail && !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(3, 0, 2)) {
						return
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				pinned := publictest.PinnedSession(t, db)
				defer func() { _ = pinned.Close() }()
				r, err := oceanbase.ExecBatch(context.Background(), pinned, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
				if r.Items[0].InsertID != testMaxUint64Text || !r.Items[0].InsertIDKnown {
					t.Fatal(r)
				}
				if r.Strategy != oceanbase.BatchRewrite || r.Items[0].Status != oceanbase.BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 {
					t.Fatalf("results: %+v %v", r, err)
				}
				if fail {
					var be *oceanbase.BatchError
					var server *oceanbase.Error
					if !errors.As(err, &be) || !errors.As(err, &server) || be.Index != 1 || be.Completed != 1 || server.Code != 1062 || r.Items[1].Status != oceanbase.BatchFailed || r.Items[2].Status != oceanbase.BatchNotExecuted || !r.Items[2].Started {
						t.Fatalf("stop: %+v %v", r, err)
					}
				} else if err != nil || r.Items[1].RowsAffected != 2 || r.Items[2].RowsAffected != 3 || r.Items[2].Status != oceanbase.BatchSucceeded {
					t.Fatalf("success: %+v %v", r, err)
				}
				if err = pinned.PingContext(context.Background()); err != nil {
					t.Fatal("response boundary lost", err)
				}
			})
		}
	}
}

func TestBatchRewriteChunks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  oceanbase.BatchOptions
		limit int
	}{
		{"rows", oceanbase.BatchOptions{ChunkRows: 2}, 0},
		{"parameters", oceanbase.BatchOptions{ChunkParameters: 2}, 0},
		{"results", oceanbase.BatchOptions{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true, Limits: oceanbase.Limits{MaxResultItems: tc.limit}}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				for _, query := range []string{batchRewritePair, "INSERT INTO t VALUES ( 3 )\n;\nINSERT INTO t VALUES ( 4 )"} {
					if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(math.MaxUint64, 0, 2)) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tc.opts.Strategy = oceanbase.BatchRewrite
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, tc.opts)
			if err != nil || r.Strategy != oceanbase.BatchRewrite || batchSucceeded(&r) != 4 || r.Items[1].RowsAffectedKnown || r.Items[3].RowsAffectedKnown {
				t.Fatalf("chunk accounting: %+v %v", r, err)
			}
		})
	}
}

func TestBatchRewriteMessageLimit(t *testing.T) {
	const query = "INSERT INTO batch_table_with_a_deliberately_long_name_for_single_statement_packet_budget VALUES (?)"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true, Limits: oceanbase.Limits{MaxMessageBytes: 192}}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{"INSERT INTO batch_table_with_a_deliberately_long_name_for_single_statement_packet_budget VALUES ( 1 )", "INSERT INTO batch_table_with_a_deliberately_long_name_for_single_statement_packet_budget VALUES ( 2 )"} {
			if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, q) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteValidationOrder(t *testing.T) {
	for _, serverFails := range []bool{false, true} {
		t.Run(fmt.Sprint(serverFails), func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair) {
					return
				}
				if serverFails {
					if !testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) {
						return
					}
				} else if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2)) || !serveBatchRewriteGuard(ctx, t, p, "0", 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3, 4}, {5}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || r.Items[3].Status != oceanbase.BatchNotExecuted || r.Items[3].Started {
				t.Fatal(r, err)
			}
			if serverFails {
				if be.Index != 0 || be.Completed != 0 || r.Items[1].Status != oceanbase.BatchNotExecuted || r.Items[2].Err != nil {
					t.Fatalf("later local error masked first server error: %+v %v", r, err)
				}
			} else if be.Index != 2 || be.Completed != 2 || !errors.Is(err, oceanbase.ErrBind) || r.Items[2].Status != oceanbase.BatchFailed || r.Items[2].Started {
				t.Fatalf("valid prefix was lost: %+v %v", r, err)
			}
		})
	}
}

func TestBatchRewriteFallback(t *testing.T) {
	for _, name := range []string{"permission-off", publictest.TransactionOracleLabel, "keep-going", "server-merge-on", "missing-view", "missing-parameter", "chunk-one"} {
		t.Run(name, func(t *testing.T) {
			cfg := oceanbase.Config{AllowMultiStatements: name != "permission-off"}
			status := byte(2)
			if name == publictest.TransactionOracleLabel {
				cfg.Mode, status = oceanbase.ModeOracle, 6
			}
			opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite, ContinueOnError: name == "keep-going"}
			if name == "chunk-one" {
				opts.ChunkRows = 1
			}
			db := publictest.BatchRewriteDB(t, cfg, name, func(ctx context.Context, p *wire.PacketIO) {
				if name == "missing-parameter" && (!testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) || !testpeer.SendPackets(ctx, t, p,
					[]byte{1}, testpeer.TextColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, status, 0},
					[]byte{251}, []byte{254, 0, 0, status, 0})) {
					return
				}
				if name == "server-merge-on" && !serveBatchRewriteGuard(ctx, t, p, "1", status) {
					return
				}
				if name == "missing-view" && (!testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) || !testpeer.SendPackets(ctx, t, p, []byte{255, 122, 4, '#', '4', '2', 'S', '0', '2', 'n', 'o'})) {
					return
				}
				for i := int64(1); i <= 2; i++ {
					if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, i)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, opts)
			if err != nil || r.Strategy != oceanbase.BatchSequential || batchSucceeded(&r) != 2 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchRewriteUnadvertisedMultiStatements(t *testing.T) {
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, "no-offer", func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if err != nil || r.Strategy != oceanbase.BatchRewrite || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteLineCommentsAndLiterals(t *testing.T) {
	const query = "INSERT INTO t VALUES (?) -- comment"
	const value = "'); DELETE FROM t; --\n"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		const want = "INSERT INTO t VALUES ( _utf8mb4 X'27293b2044454c4554452046524f4d20743b202d2d0a' ) -- comment\n;\nINSERT INTO t VALUES ( NULL ) -- comment"
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{value}, {nil}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteCharset(t *testing.T) {
	const query = "INSERT INTO `中文` VALUES (?)"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true, Session: oceanbase.SessionOptions{Charset: localFilenameCharset}}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		const want = "INSERT INTO `\xd6\xd0\xce\xc4` VALUES ( _gbk X'd6d0cec4' )\n;\nINSERT INTO `\xd6\xd0\xce\xc4` VALUES ( X'0001ff' )"
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{"中文"}, {[]byte{0, 1, 255}}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteTxBoundary(t *testing.T) {
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !serveBatchRewriteGuard(ctx, t, p, "0", 3) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 11), publictest.CommandPacket(1, 0, 3)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
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
	r, err := oceanbase.ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if err != nil || r.Strategy != oceanbase.BatchRewrite || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchRewriteGuardChanges(t *testing.T) {
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2)) || !serveBatchRewriteGuard(ctx, t, p, "1", 2) {
			return
		}
		for _, q := range []string{"INSERT INTO t VALUES ( 3 )", "INSERT INTO t VALUES ( 4 )"} {
			if !testpeer.ReadQuery(ctx, t, p, q) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite, ChunkRows: 2})
	if err != nil || batchSucceeded(&r) != 4 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteGuardRowError(t *testing.T) {
	for _, framing := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		for _, prefix := range []bool{false, true} {
			for _, afterRow := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/prefix=%t/after-row=%t", framing, prefix, afterRow), func(t *testing.T) {
					testBatchRewriteGuardRowError(t, framing, prefix, afterRow)
				})
			}
		}
	}
}

func testBatchRewriteGuardRowError(t *testing.T, framing string, prefix, afterRow bool) {
	t.Helper()
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if prefix && (!serveBatchRewriteGuard(ctx, t, p, "0", 2) || !testpeer.ReadQuery(ctx, t, p, batchRewritePair) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), publictest.CommandPacket(1, 0, 2))) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		if afterRow && !testpeer.SendPackets(ctx, t, p, []byte{1, '0'}) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), pinned, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite, ChunkRows: 2})
	want := oceanbase.BatchSequential
	completed := 0
	if prefix {
		want = oceanbase.BatchRewrite
		completed = 2
	}
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1045 || r.Strategy != want || batchSucceeded(&r) != completed {
		t.Fatal(r, err)
	}
	for _, item := range r.Items[:completed] {
		if !item.Started || !item.RowsAffectedKnown || item.RowsAffected != 1 || item.Err != nil {
			t.Fatal("guard failure lost earlier acknowledgement", item)
		}
	}
	for _, item := range r.Items[completed:] {
		if item.Started {
			t.Fatal("guard failure sent more DML")
		}
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal("guard error discarded the original session", err)
	}
}

func TestBatchRewriteCancellation(t *testing.T) {
	for _, beforeBusiness := range []bool{false, true} {
		t.Run(fmt.Sprint(beforeBusiness), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(peerCtx context.Context, p *wire.PacketIO) {
				if beforeBusiness {
					if !testpeer.ReadQuery(peerCtx, t, p, batchRewriteGuard) {
						return
					}
				} else if !serveBatchRewriteGuard(peerCtx, t, p, "0", 2) || !testpeer.ReadQuery(peerCtx, t, p, batchRewritePair) {
					return
				}
				cancel()
				if _, err := p.ReadMessage(peerCtx); err == nil {
					t.Error("canceled operation sent another command")
				}
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite, ChunkRows: 2})
			var be *oceanbase.BatchError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &be) || be.Completed != 0 || r.Items[2].Status != oceanbase.BatchNotExecuted || r.Items[2].Started {
				t.Fatal(r, err)
			}
			for i := range 2 {
				want := oceanbase.BatchUnknown
				if beforeBusiness {
					want = oceanbase.BatchNotExecuted
				}
				if r.Items[i].Status != want || r.Items[i].Started == beforeBusiness {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestBatchRewriteModeChangeRejectsNewStatements(t *testing.T) {
	const query = "INSERT INTO t VALUES (?, 'a\\'; DELETE FROM t; -- trailing')"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{AllowMultiStatements: true}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, batchRewriteGuard) || !testpeer.SendPackets(ctx, t, p,
			[]byte{1}, testpeer.TextColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, 2, 2}, []byte{1, '0'}, []byte{254, 0, 0, 2, 2}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	if !errors.Is(err, oceanbase.ErrBind) || r.Items[0].Started || r.Items[1].Started {
		t.Fatalf("changed SQL mode introduced a second statement: %+v %v", r, err)
	}
}
