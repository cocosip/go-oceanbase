package batch_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"math"
	"strings"
	"testing"
)

const (
	arrayFallbackAutocommit = "array-autocommit"
	arrayFallbackUnsigned   = "array-unsigned"
	arrayFallbackReadOnly   = "array-read-only"
	arrayFallbackValuer     = "array-valuer"
)

func batchArrayPacket(t testing.TB, values []any, options wire.ExecuteOptions) []byte {
	t.Helper()
	rows := make([][]wire.Parameter, len(values))
	for i, value := range values {
		normalized, err := normalizeArgument(value)
		if err != nil {
			t.Fatal(err)
		}
		parameter, err := valuecodec.EncodeParameter(normalized, oceanbase.ModeOracle, oceanbase.SessionOptions{}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = []wire.Parameter{parameter}
	}
	packet, err := wire.EncodeStmtExecuteArray(7, rows, 1<<20, options)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func batchArrayDiagnostic(position, code uint64, message string) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{0, 0}, position)
	data = binary.LittleEndian.AppendUint64(data, code)
	return append(append(data, byte(len(message))), message...)
}

func serveBatchArrayDiagnostics(ctx context.Context, t *testing.T, p *wire.PacketIO, rows ...[]byte) bool {
	t.Helper()
	return testpeer.SendPackets(ctx, t, p, batchArrayDiagnostics(rows...)...)
}

func batchArrayDiagnostics(rows ...[]byte) [][]byte {
	packets := [][]byte{{3}, testpeer.TextColumn("sql_no", 8, 45, 0), testpeer.TextColumn("error_code", 8, 45, 0), testpeer.TextColumn("error_message", 253, 45, 0), {254, 0, 0, 13, 0}}
	packets = append(packets, rows...)
	packets = append(packets, []byte{254, 0, 0, 13, 0})
	return packets
}

func serveBatchArrayBegin(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	return publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) && publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 5)
}

func serveBatchArrayEnd(ctx context.Context, t *testing.T, p *wire.PacketIO) {
	t.Helper()
	if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func TestBatchArrayBinaryDoubleRange(t *testing.T) {
	db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveBatchArrayBegin(ctx, t, p) {
			return
		}
		packet := []byte{23, 7, 0, 0, 0, 24, 1, 0, 0, 0, 0, 1,
			160, 0, 0, 0, 0, 5, 3, 0,
			255, 255, 255, 255, 255, 255, 239, 127,
			255, 255, 255, 255, 255, 255, 239, 255,
			1, 0, 0, 0, 0, 0, 0, 0}
		if !testpeer.ReadCommand(ctx, t, p, packet) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(3, 0, 5)) {
			return
		}
		serveBatchArrayEnd(ctx, t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := oceanbase.ExecBatch(t.Context(), tx, testBatchSQL, [][]any{{math.MaxFloat64}, {-math.MaxFloat64}, {math.SmallestNonzeroFloat64}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true})
	if err != nil || result.Strategy != oceanbase.BatchArrayBinding || batchSucceeded(&result) != 3 {
		t.Fatalf("binary double array outcomes: %+v %v", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBatchArrayResults(t *testing.T) {
	for _, variant := range []string{testpeer.Standard, testpeer.Compression, testpeer.OB20} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprint(variant, "/", failed), func(t *testing.T) {
				options := wire.ExecuteOptions{UseChecksum: true, SQLChecksum: wire.SQLChecksum([]byte(testBatchSQL))}
				db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle, Execution: oceanbase.ExecutionOptions{StatementChecksum: true, ReuseParameterTypes: true}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveBatchArrayBegin(ctx, t, p) || !testpeer.ReadCommand(ctx, t, p, batchArrayPacket(t, []any{1, 2}, options)) {
						return
					}
					if failed && !serveBatchArrayDiagnostics(ctx, t, p, batchArrayDiagnostic(1, 1062, "duplicate")) {
						return
					}
					if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(2, 17, 5)) || !testpeer.ReadCommand(ctx, t, p, batchArrayPacket(t, []any{3}, options)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(3, math.MaxUint64, 5)) {
						return
					}
					serveBatchArrayEnd(ctx, t, p)
				})
				defer func() { _ = db.Close() }()
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				r, err := oceanbase.ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkRows: 2})
				checkBatchInsertID(t, r.Items[0], "")
				checkBatchInsertID(t, r.Items[1], "")
				checkBatchInsertID(t, r.Items[2], testMaxUint64Text)
				if r.Strategy != oceanbase.BatchArrayBinding || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[0].RowsAffectedKnown || r.Items[2].Status != oceanbase.BatchSucceeded || !r.Items[2].RowsAffectedKnown || r.Items[2].RowsAffected != 3 {
					t.Fatalf("array outcomes: %+v %v", r, err)
				}
				if failed {
					var batch *oceanbase.BatchError
					var server *oceanbase.Error
					if !errors.As(err, &batch) || batch.Index != 1 || batch.Completed != 2 || !errors.As(err, &server) || server.Code != 1062 || server.SQLState != "" || r.Items[1].Status != oceanbase.BatchFailed {
						t.Fatalf("array errors: %+v %v", r, err)
					}
				} else if err != nil || r.Items[1].Status != oceanbase.BatchSucceeded || r.Items[1].RowsAffectedKnown {
					t.Fatalf("array success: %+v %v", r, err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal("original transaction lost", err)
				}
				if err = db.PingContext(context.Background()); err != nil {
					t.Fatal("response boundary lost", err)
				}
			})
		}
	}
}

func TestBatchArrayChunks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   [][]any
		chunks [][]any
		opts   oceanbase.BatchOptions
		limits oceanbase.Limits
		bad    int
	}{
		{"rows", [][]any{{1}, {2}, {3}, {4}}, [][]any{{1, 2}, {3, 4}}, oceanbase.BatchOptions{ChunkRows: 2}, oceanbase.Limits{}, -1},
		{"parameters", [][]any{{1}, {2}, {3}, {4}}, [][]any{{1, 2}, {3, 4}}, oceanbase.BatchOptions{ChunkParameters: 2}, oceanbase.Limits{}, -1},
		{"results", [][]any{{1}, {2}}, [][]any{{1}, {2}}, oceanbase.BatchOptions{}, oceanbase.Limits{MaxResultItems: 1}, -1},
		{"types", [][]any{{1}, {2}, {"a"}, {"b"}, {nil}, {nil}}, [][]any{{1, 2}, {"a", "b"}, {nil, nil}}, oceanbase.BatchOptions{}, oceanbase.Limits{}, -1},
		{"bytes", [][]any{{strings.Repeat("a", 70)}, {strings.Repeat("b", 70)}, {strings.Repeat("c", 70)}}, [][]any{{strings.Repeat("a", 70), strings.Repeat("b", 70)}, {strings.Repeat("c", 70)}}, oceanbase.BatchOptions{}, oceanbase.Limits{MaxMessageBytes: 192}, -1},
		{"local-error", [][]any{{"a"}, {"b"}, {strings.Repeat("x", 200)}, {"c"}, {"d"}}, [][]any{{"a", "b"}, {"c", "d"}}, oceanbase.BatchOptions{}, oceanbase.Limits{MaxMessageBytes: 192}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle, Limits: tc.limits}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchArrayBegin(ctx, t, p) {
					return
				}
				for _, values := range tc.chunks {
					if !testpeer.ReadCommand(ctx, t, p, batchArrayPacket(t, values, wire.ExecuteOptions{})) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(uint64(len(values)), 0, 5)) {
						return
					}
				}
				serveBatchArrayEnd(ctx, t, p)
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			tc.opts.Strategy, tc.opts.ContinueOnError = oceanbase.BatchArrayBinding, true
			r, err := oceanbase.ExecBatch(context.Background(), tx, testBatchSQL, tc.rows, tc.opts)
			if r.Strategy != oceanbase.BatchArrayBinding || (err != nil) != (tc.bad >= 0) {
				t.Fatalf("chunks: %+v %v", r, err)
			}
			for i, item := range r.Items {
				if i == tc.bad {
					if item.Status != oceanbase.BatchFailed || item.Started || !errors.Is(item.Err, wire.ErrMessageTooLarge) {
						t.Fatalf("local input: %+v", item)
					}
				} else if item.Status != oceanbase.BatchSucceeded || !item.Started {
					t.Fatalf("chunk item %d: %+v", i, item)
				}
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchArrayFallback(t *testing.T) {
	for _, reason := range []string{arrayFallbackAutocommit, "array-stop", arrayFallbackReadOnly, "array-unknown-version", "array-old-version", testClientStrategy, arrayFallbackUnsigned, arrayFallbackValuer} {
		t.Run(reason, func(t *testing.T) {
			status := byte(5)
			if reason == arrayFallbackAutocommit {
				status = 6
			}
			cfg := oceanbase.Config{Mode: oceanbase.ModeOracle}
			if reason == testClientStrategy {
				cfg.PrepareMode = oceanbase.PrepareClient
			}
			version := "4.3.5.1"
			switch reason {
			case "array-unknown-version":
				version = ""
			case "array-old-version":
				version = "4.2.0"
			}
			db := publictest.QueryVersionDB(t, version, func(ctx context.Context, p *wire.PacketIO) {
				serveBatchArrayFallback(ctx, t, p, reason, status)
			}, func(options *oceanbase.Config) { *options = cfg })
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			var exec oceanbase.Execer = pinned
			var transaction *sql.Tx
			if reason != arrayFallbackAutocommit {
				var err error
				transaction, err = pinned.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: reason == arrayFallbackReadOnly})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = transaction.Rollback() }()
				exec = transaction
			}
			rows := [][]any{{1}, {2}}
			var calls int
			switch reason {
			case arrayFallbackUnsigned:
				rows = [][]any{{uint64(1)}, {uint64(2)}}
			case arrayFallbackValuer:
				rows = [][]any{{countingValuer{calls: &calls}}, {countingValuer{calls: &calls}}}
			}
			r, err := oceanbase.ExecBatch(context.Background(), exec, testBatchSQL, rows, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: reason != "array-stop"})
			if err != nil || r.Strategy != oceanbase.BatchSequential || batchSucceeded(&r) != 2 || reason == arrayFallbackValuer && calls != 2 {
				t.Fatalf("fallback: %+v %v, valuer calls=%d", r, err, calls)
			}
			if transaction != nil {
				if err = transaction.Rollback(); err != nil {
					t.Fatal(err)
				}
			}
			if err = pinned.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveBatchArrayFallback(ctx context.Context, t *testing.T, p *wire.PacketIO, reason string, status byte) {
	t.Helper()
	if reason != arrayFallbackAutocommit {
		access := publictest.TestOracleReadWrite
		if reason == arrayFallbackReadOnly {
			access = publictest.TransactionOracleReadOnly
		}
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: access, Status: 5}}) {
			return
		}
	}
	for i := 1; i <= 2; i++ {
		if reason == testClientStrategy {
			if !testpeer.ReadQuery(ctx, t, p, fmt.Sprintf("INSERT INTO t VALUES ( %d )", i)) {
				return
			}
		} else {
			if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
				return
			}
			payload := batchExecutePacket(t, int64(i))
			if reason == arrayFallbackValuer {
				payload = batchExecutePacket(t, 42)
			}
			if reason == arrayFallbackUnsigned {
				payload[13] = 128
			}
			if !testpeer.ReadCommand(ctx, t, p, payload) {
				return
			}
		}
		if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) || reason != testClientStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
	}
	if reason != arrayFallbackAutocommit && !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
		return
	}
	if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func TestBatchArrayPhysicalParameterBudget(t *testing.T) {
	const query = "INSERT INTO t VALUES (:value,:value)"
	db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle}, testpeer.Standard, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServePrepareStatus(ctx, t, p, "INSERT INTO t VALUES (?,?)", 7, 2, nil, 5) {
			return
		}
		for _, value := range []uint64{1, 2} {
			parameter := wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, value)}
			payload, err := wire.EncodeStmtExecuteArray(7, [][]wire.Parameter{{parameter, parameter}}, 4096, wire.ExecuteOptions{})
			if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 5)) {
				return
			}
		}
		serveBatchArrayEnd(ctx, t, p)
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := oceanbase.ExecBatch(context.Background(), tx, query, [][]any{{sql.Named("value", 1)}, {sql.Named("value", 2)}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkParameters: 3})
	if err != nil || r.Strategy != oceanbase.BatchArrayBinding || batchSucceeded(&r) != 2 || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || !r.Items[1].RowsAffectedKnown {
		t.Fatalf("expanded physical slots: %+v %v", r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
