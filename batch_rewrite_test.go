package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const batchRewriteGuard = "SELECT @@SESSION.ob_enable_batched_multi_statement"
const batchRewritePair = "INSERT INTO t VALUES ( 1 )\n;\nINSERT INTO t VALUES ( 2 )"
const batchRewriteEarlyEnd = "early-end"
const batchRewriteUnexpectedMore = "unexpected-more"

func batchRewriteDB(t *testing.T, cfg Config, variant string, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if cfg.Mode == ModeOracle {
		status = 6
	}
	cfg.User = testUser
	cfg.Protocol = ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff}
	switch variant {
	case testCompression:
		cfg.Protocol.Compression = FeatureRequired
	case testOB20:
		cfg.Protocol.OB20 = FeatureRequired
	}
	cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := protocolGreeting(variant == testCompression)
		// The high capability word starts after the low word, charset and status.
		start := 1
		for greeting[start] != 0 {
			start++
		}
		start += 1 + 4 + 8 + 1 + 2 + 1 + 2
		if variant != "no-offer" {
			binary.LittleEndian.PutUint16(greeting[start:], binary.LittleEndian.Uint16(greeting[start:])|uint16(wire.ClientMultiStatements>>16))
		}
		if !sendPackets(ctx, t, p, greeting) {
			return
		}
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		vars := map[string]string{}
		if variant == testOB20 {
			vars[testProxyCapability] = "33024"
		}
		if !sendPackets(ctx, t, p, protocolOK(status, vars)) {
			return
		}
		switch variant {
		case testCompression:
			if err := p.EnableCompression(); err != nil {
				t.Error(err)
				return
			}
		case testOB20:
			if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
				t.Error(err)
				return
			}
		}
		ready := false
		if cfg.Session.Charset == localFilenameCharset {
			ready = serveOK(ctx, t, p, []byte("\x03SET NAMES gbk"), status) && serveOK(ctx, t, p, []byte("\x03"+testAutocommitOn), status) && serveVersion(ctx, t, p, status, false) && serveOK(ctx, t, p, []byte("\x03"+transactionUTCZone), status)
		} else {
			ready = serveBaseline(ctx, t, p, status)
		}
		if ready {
			serve(context.Background(), p)
		}
	})
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func serveBatchRewriteGuard(ctx context.Context, t testing.TB, p *wire.PacketIO, value string, status byte) bool {
	t.Helper()
	return readQuery(ctx, t, p, batchRewriteGuard) && sendPackets(ctx, t, p,
		[]byte{1}, textColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, status, 0},
		append([]byte{byte(len(value))}, value...), []byte{254, 0, 0, status, 0})
}

func TestBatchRewriteResults(t *testing.T) {
	for _, variant := range []string{testStandard, testCompression, testOB20} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprint(variant, "/", fail), func(t *testing.T) {
				db := batchRewriteDB(t, Config{AllowMultiStatements: true}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair+"\n;\nINSERT INTO t VALUES ( 3 )") {
						return
					}
					second := commandPacket(2, 0, 10)
					if fail {
						second = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if !sendPackets(ctx, t, p, commandPacket(1, 0, 10), second) {
						return
					}
					if !fail && !sendPackets(ctx, t, p, commandPacket(3, 0, 2)) {
						return
					}
					if !serveOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				pinned := pinnedSession(t, db)
				defer func() { _ = pinned.Close() }()
				r, err := ExecBatch(context.Background(), pinned, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchRewrite})
				if r.Strategy != BatchRewrite || r.Items[0].Status != BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 {
					t.Fatalf("results: %+v %v", r, err)
				}
				if fail {
					var be *BatchError
					var server *Error
					if !errors.As(err, &be) || !errors.As(err, &server) || be.Index != 1 || be.Completed != 1 || server.Code != 1062 || r.Items[1].Status != BatchFailed || r.Items[2].Status != BatchNotExecuted || !r.Items[2].Started {
						t.Fatalf("stop: %+v %v", r, err)
					}
				} else if err != nil || r.Items[1].RowsAffected != 2 || r.Items[2].RowsAffected != 3 || r.Items[2].Status != BatchSucceeded {
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
		opts  BatchOptions
		limit int
	}{
		{"rows", BatchOptions{ChunkRows: 2}, 0},
		{"parameters", BatchOptions{ChunkParameters: 2}, 0},
		{"results", BatchOptions{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := batchRewriteDB(t, Config{AllowMultiStatements: true, Limits: Limits{MaxResultItems: tc.limit}}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				for _, query := range []string{batchRewritePair, "INSERT INTO t VALUES ( 3 )\n;\nINSERT INTO t VALUES ( 4 )"} {
					if !serveBatchRewriteGuard(ctx, t, p, "OFF", 2) || !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(math.MaxUint64, 0, 2)) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tc.opts.Strategy = BatchRewrite
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, tc.opts)
			if err != nil || r.Strategy != BatchRewrite || batchSucceeded(&r) != 4 || r.Items[1].RowsAffectedKnown || r.Items[3].RowsAffectedKnown {
				t.Fatalf("chunk accounting: %+v %v", r, err)
			}
		})
	}
}

func TestBatchRewriteMessageLimit(t *testing.T) {
	const query = "INSERT INTO batch_table_with_a_deliberately_long_name VALUES (?)"
	db := batchRewriteDB(t, Config{AllowMultiStatements: true, Limits: Limits{MaxMessageBytes: 128}}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		for _, q := range []string{"INSERT INTO batch_table_with_a_deliberately_long_name VALUES ( 1 )", "INSERT INTO batch_table_with_a_deliberately_long_name VALUES ( 2 )"} {
			if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, q) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteValidationOrder(t *testing.T) {
	for _, serverFails := range []bool{false, true} {
		t.Run(fmt.Sprint(serverFails), func(t *testing.T) {
			db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair) {
					return
				}
				if serverFails {
					if !sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) {
						return
					}
				} else if !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 2)) || !serveBatchRewriteGuard(ctx, t, p, "0", 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3, 4}, {5}}, BatchOptions{Strategy: BatchRewrite})
			var be *BatchError
			if !errors.As(err, &be) || r.Items[3].Status != BatchNotExecuted || r.Items[3].Started {
				t.Fatal(r, err)
			}
			if serverFails {
				if be.Index != 0 || be.Completed != 0 || r.Items[1].Status != BatchNotExecuted || r.Items[2].Err != nil {
					t.Fatalf("later local error masked first server error: %+v %v", r, err)
				}
			} else if be.Index != 2 || be.Completed != 2 || !errors.Is(err, ErrBind) || r.Items[2].Status != BatchFailed || r.Items[2].Started {
				t.Fatalf("valid prefix was lost: %+v %v", r, err)
			}
		})
	}
}

func TestBatchRewriteFallback(t *testing.T) {
	for _, name := range []string{"permission-off", "no-offer", transactionOracleLabel, "keep-going", "server-merge-on", "missing-variable", "chunk-one"} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{AllowMultiStatements: name != "permission-off"}
			status := byte(2)
			if name == transactionOracleLabel {
				cfg.Mode, status = ModeOracle, 6
			}
			opts := BatchOptions{Strategy: BatchRewrite, ContinueOnError: name == "keep-going"}
			if name == "chunk-one" {
				opts.ChunkRows = 1
			}
			db := batchRewriteDB(t, cfg, name, func(ctx context.Context, p *wire.PacketIO) {
				if name == "server-merge-on" && !serveBatchRewriteGuard(ctx, t, p, "1", status) {
					return
				}
				if name == "missing-variable" && (!readQuery(ctx, t, p, batchRewriteGuard) || !sendPackets(ctx, t, p, []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'n', 'o'})) {
					return
				}
				for i := int64(1); i <= 2; i++ {
					if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !readCommand(ctx, t, p, batchExecutePacket(t, i)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, opts)
			if err != nil || r.Strategy != BatchSequential || batchSucceeded(&r) != 2 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchRewriteUncertainty(t *testing.T) {
	for _, name := range []string{"peer-closed", "short-ok", "rowset", batchRewriteEarlyEnd, batchRewriteUnexpectedMore} {
		t.Run(name, func(t *testing.T) {
			db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair+"\n;\nINSERT INTO t VALUES ( 3 )") {
					return
				}
				if name == batchRewriteEarlyEnd {
					_ = sendPackets(ctx, t, p, commandPacket(3, 0, 2))
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(1, 0, 10)) {
					return
				}
				switch name {
				case "short-ok":
					_ = sendPackets(ctx, t, p, []byte{0})
				case "rowset":
					_ = sendPackets(ctx, t, p, []byte{1})
				case batchRewriteUnexpectedMore:
					_ = sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 10))
				}
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, BatchOptions{Strategy: BatchRewrite, ChunkRows: 3})
			var be *BatchError
			if !errors.As(err, &be) || r.Items[3].Status != BatchNotExecuted || r.Items[3].Started {
				t.Fatal(r, err)
			}
			acknowledged := 1
			switch name {
			case batchRewriteEarlyEnd:
				acknowledged = 0
			case batchRewriteUnexpectedMore:
				acknowledged = 2
			}
			if be.Completed != acknowledged || be.Index != acknowledged {
				t.Fatal(r, err)
			}
			for i := acknowledged; i < 3; i++ {
				if r.Items[i].Status != BatchUnknown || !r.Items[i].Started {
					t.Fatal(r)
				}
			}
			if name == "peer-closed" {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, wire.ErrMalformed) {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchRewriteLineCommentsAndLiterals(t *testing.T) {
	const query = "INSERT INTO t VALUES (?) -- comment"
	const value = "'); DELETE FROM t; --\n"
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		const want = "INSERT INTO t VALUES ( _utf8mb4 X'27293b2044454c4554452046524f4d20743b202d2d0a' ) -- comment\n;\nINSERT INTO t VALUES ( NULL ) -- comment"
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, want) || !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 2)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, query, [][]any{{value}, {nil}}, BatchOptions{Strategy: BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteCharset(t *testing.T) {
	const query = "INSERT INTO `中文` VALUES (?)"
	db := batchRewriteDB(t, Config{AllowMultiStatements: true, Session: SessionOptions{Charset: localFilenameCharset}}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		const want = "INSERT INTO `\xd6\xd0\xce\xc4` VALUES ( _gbk X'd6d0cec4' )\n;\nINSERT INTO `\xd6\xd0\xce\xc4` VALUES ( X'0001ff' )"
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, want) || !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 2)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, query, [][]any{{"中文"}, {[]byte{0, 1, 255}}}, BatchOptions{Strategy: BatchRewrite})
	if err != nil || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteTxBoundary(t *testing.T) {
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveBatchRewriteGuard(ctx, t, p, "0", 3) || !readQuery(ctx, t, p, batchRewritePair) || !sendPackets(ctx, t, p, commandPacket(1, 0, 11), commandPacket(1, 0, 3)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
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
	r, err := ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchRewrite})
	if err != nil || r.Strategy != BatchRewrite || batchSucceeded(&r) != 2 {
		t.Fatal(r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchRewriteGuardChanges(t *testing.T) {
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair) || !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 2)) || !serveBatchRewriteGuard(ctx, t, p, "1", 2) {
			return
		}
		for _, q := range []string{"INSERT INTO t VALUES ( 3 )", "INSERT INTO t VALUES ( 4 )"} {
			if !readQuery(ctx, t, p, q) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, BatchOptions{Strategy: BatchRewrite, ChunkRows: 2})
	if err != nil || batchSucceeded(&r) != 4 {
		t.Fatal(r, err)
	}
}

func TestBatchRewriteGuardRowError(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
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
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if prefix && (!serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair) || !sendPackets(ctx, t, p, commandPacket(1, 0, 10), commandPacket(1, 0, 2))) {
			return
		}
		if !readQuery(ctx, t, p, batchRewriteGuard) || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		if afterRow && !sendPackets(ctx, t, p, []byte{1, '0'}) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if prefix {
			for _, q := range []string{"INSERT INTO t VALUES ( 3 )", "INSERT INTO t VALUES ( 4 )"} {
				if !readQuery(ctx, t, p, q) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
					return
				}
			}
		} else {
			for i := int64(1); i <= 4; i++ {
				if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 2) || !readCommand(ctx, t, p, batchExecutePacket(t, i)) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
					return
				}
			}
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	r, err := ExecBatch(context.Background(), pinned, testBatchSQL, [][]any{{1}, {2}, {3}, {4}}, BatchOptions{Strategy: BatchRewrite, ChunkRows: 2})
	want := BatchSequential
	if prefix {
		want = BatchRewrite
	}
	if err != nil || r.Strategy != want || batchSucceeded(&r) != 4 {
		t.Fatal(r, err)
	}
	for _, item := range r.Items {
		if !item.Started || !item.RowsAffectedKnown || item.RowsAffected != 1 || item.Err != nil {
			t.Fatal("fallback lost per-item acknowledgement", item)
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
			db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(peerCtx context.Context, p *wire.PacketIO) {
				if beforeBusiness {
					if !readQuery(peerCtx, t, p, batchRewriteGuard) {
						return
					}
				} else if !serveBatchRewriteGuard(peerCtx, t, p, "0", 2) || !readQuery(peerCtx, t, p, batchRewritePair) {
					return
				}
				cancel()
				if _, err := p.ReadMessage(peerCtx); err == nil {
					t.Error("canceled operation sent another command")
				}
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchRewrite, ChunkRows: 2})
			var be *BatchError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &be) || be.Completed != 0 || r.Items[2].Status != BatchNotExecuted || r.Items[2].Started {
				t.Fatal(r, err)
			}
			for i := range 2 {
				want := BatchUnknown
				if beforeBusiness {
					want = BatchNotExecuted
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
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, batchRewriteGuard) || !sendPackets(ctx, t, p,
			[]byte{1}, textColumn("server-merge-on", 253, 45, 0), []byte{254, 0, 0, 2, 2}, []byte{1, '0'}, []byte{254, 0, 0, 2, 2}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, query, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchRewrite})
	if !errors.Is(err, ErrBind) || r.Items[0].Started || r.Items[1].Started {
		t.Fatalf("changed SQL mode introduced a second statement: %+v %v", r, err)
	}
}

func TestBatchRewriteAcknowledgedErrorAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := batchRewriteDB(t, Config{AllowMultiStatements: true}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchRewriteGuard(ctx, t, p, "0", 2) || !readQuery(ctx, t, p, batchRewritePair) {
			return
		}
		_ = sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'})
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	if err := pinned.Raw(func(raw any) error {
		c := raw.(*conn)
		return c.session.packets.ReplaceConn(&batchCancelReadConn{Conn: c.session.raw, response: []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}, cancel: cancel})
	}); err != nil {
		t.Fatal(err)
	}
	r, err := ExecBatch(ctx, pinned, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchRewrite})
	var server *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &server) || server.Code != 1062 || r.Items[0].Status != BatchFailed || r.Items[1].Status != BatchNotExecuted {
		t.Fatalf("acknowledged server error or cancellation lost: %+v %v", r, err)
	}
}
