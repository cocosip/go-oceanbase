package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	arrayFallbackAutocommit = "array-autocommit"
	arrayFallbackUnsigned   = "array-unsigned"
	arrayFallbackReadOnly   = "array-read-only"
	arrayFallbackValuer     = "array-valuer"
)

func batchArrayPacket(t *testing.T, values []any, options wire.ExecuteOptions) []byte {
	t.Helper()
	rows := make([][]wire.Parameter, len(values))
	for i, value := range values {
		normalized, err := convertArgument(value)
		if err != nil {
			t.Fatal(err)
		}
		parameter, err := encodeParameter(normalized, ModeOracle, SessionOptions{}, 1<<20)
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
	return sendPackets(ctx, t, p, batchArrayDiagnostics(rows...)...)
}

func batchArrayDiagnostics(rows ...[]byte) [][]byte {
	packets := [][]byte{{3}, textColumn("sql_no", 8, 45, 0), textColumn("error_code", 8, 45, 0), textColumn("error_message", 253, 45, 0), {254, 0, 0, 13, 0}}
	packets = append(packets, rows...)
	packets = append(packets, []byte{254, 0, 0, 13, 0})
	return packets
}

func serveBatchArrayBegin(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	return serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) && servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 5)
}

func serveBatchArrayEnd(ctx context.Context, t *testing.T, p *wire.PacketIO) {
	t.Helper()
	if !readCommand(ctx, t, p, stmtPacket(25, 7)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) || !serveOK(ctx, t, p, []byte{14}, 6) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func TestBatchArrayResults(t *testing.T) {
	for _, variant := range []string{testStandard, testCompression, testOB20} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprint(variant, "/", failed), func(t *testing.T) {
				options := wire.ExecuteOptions{UseChecksum: true, SQLChecksum: wire.SQLChecksum([]byte(testBatchSQL))}
				db := batchRewriteDB(t, Config{Mode: ModeOracle, Execution: ExecutionOptions{StatementChecksum: true, ReuseParameterTypes: true}}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveBatchArrayBegin(ctx, t, p) || !readCommand(ctx, t, p, batchArrayPacket(t, []any{1, 2}, options)) {
						return
					}
					if failed && !serveBatchArrayDiagnostics(ctx, t, p, batchArrayDiagnostic(1, 1062, "duplicate")) {
						return
					}
					if !sendPackets(ctx, t, p, commandPacket(2, 0, 5)) || !readCommand(ctx, t, p, batchArrayPacket(t, []any{3}, options)) || !sendPackets(ctx, t, p, commandPacket(3, 0, 5)) {
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
				r, err := ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchArrayBinding, ContinueOnError: true, ChunkRows: 2})
				if r.Strategy != BatchArrayBinding || r.Items[0].Status != BatchSucceeded || r.Items[0].RowsAffectedKnown || r.Items[2].Status != BatchSucceeded || !r.Items[2].RowsAffectedKnown || r.Items[2].RowsAffected != 3 {
					t.Fatalf("array outcomes: %+v %v", r, err)
				}
				if failed {
					var batch *BatchError
					var server *Error
					if !errors.As(err, &batch) || batch.Index != 1 || batch.Completed != 2 || !errors.As(err, &server) || server.Code != 1062 || server.SQLState != "" || r.Items[1].Status != BatchFailed {
						t.Fatalf("array errors: %+v %v", r, err)
					}
				} else if err != nil || r.Items[1].Status != BatchSucceeded || r.Items[1].RowsAffectedKnown {
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

func TestBatchArrayUncertainResponse(t *testing.T) {
	for _, failure := range []string{"array-disconnect", "top-error", "duplicate", "position", "code", "array-null", "array-tail-error", "array-extra", "metadata", "metadata-out", "array-row-error", "array-metadata-error", "array-metadata-eof-error"} {
		t.Run(failure, func(t *testing.T) {
			db := batchRewriteDB(t, Config{Mode: ModeOracle}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchArrayBegin(ctx, t, p) || !readCommand(ctx, t, p, batchArrayPacket(t, []any{1, 2}, wire.ExecuteOptions{})) {
					return
				}
				row := batchArrayDiagnostic(1, 1062, "duplicate")
				var packets [][]byte
				switch failure {
				case "array-disconnect":
					return
				case "top-error":
					packets = [][]byte{{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'}}
				case "duplicate":
					packets = batchArrayDiagnostics(row, row)
				case "position":
					packets = batchArrayDiagnostics(batchArrayDiagnostic(2, 1062, "out of range"))
				case "code":
					packets = batchArrayDiagnostics(batchArrayDiagnostic(1, math.MaxUint64, "bad code"))
				case "array-null":
					packets = batchArrayDiagnostics([]byte{0, 28})
				case "metadata":
					packets = [][]byte{{1}, textColumn("wrong", 253, 45, 0), {254, 0, 0, 13, 0}}
				case "array-row-error":
					packets = batchArrayDiagnostics(row)
					packets[len(packets)-1] = []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'}
				case "array-metadata-error", "array-metadata-eof-error":
					packets = batchArrayDiagnostics(row)
					index := 2
					if strings.Contains(failure, "eof") {
						index = 4
					}
					packets = append(packets[:index], []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'})
				case "metadata-out":
					packets = append(batchArrayDiagnostics(row), commandPacket(1, 0, 5))
					packets[3][len(packets[3])-1] = 1
				case "array-tail-error":
					packets = append(batchArrayDiagnostics(row), []byte{255, 38, 4, '#', 'H', 'Y', '0', '0', '0', 'e'})
				case "array-extra":
					packets = [][]byte{commandPacket(2, 0, 13)}
				}
				for _, packet := range packets {
					if p.WriteMessage(ctx, packet) != nil {
						return
					}
				}
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			r, err := ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchArrayBinding, ContinueOnError: true, ChunkRows: 2})
			if err == nil || r.Strategy != BatchArrayBinding || r.Items[0].Status != BatchUnknown || r.Items[1].Status != BatchUnknown || r.Items[2].Status != BatchNotExecuted || r.Items[2].Started {
				t.Fatalf("uncertainty: %+v %v", r, err)
			}
			if strings.HasSuffix(failure, "error") {
				var server *Error
				if !errors.As(err, &server) || server.Code != 1062 {
					t.Fatal("server error during diagnostics lost", err)
				}
			}
		})
	}
}

func TestBatchArrayChunks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   [][]any
		chunks [][]any
		opts   BatchOptions
		limits Limits
		bad    int
	}{
		{"rows", [][]any{{1}, {2}, {3}, {4}}, [][]any{{1, 2}, {3, 4}}, BatchOptions{ChunkRows: 2}, Limits{}, -1},
		{"parameters", [][]any{{1}, {2}, {3}, {4}}, [][]any{{1, 2}, {3, 4}}, BatchOptions{ChunkParameters: 2}, Limits{}, -1},
		{"results", [][]any{{1}, {2}}, [][]any{{1}, {2}}, BatchOptions{}, Limits{MaxResultItems: 1}, -1},
		{"types", [][]any{{1}, {2}, {"a"}, {"b"}, {nil}, {nil}}, [][]any{{1, 2}, {"a", "b"}, {nil, nil}}, BatchOptions{}, Limits{}, -1},
		{"bytes", [][]any{{strings.Repeat("a", 40)}, {strings.Repeat("b", 40)}, {strings.Repeat("c", 40)}}, [][]any{{strings.Repeat("a", 40), strings.Repeat("b", 40)}, {strings.Repeat("c", 40)}}, BatchOptions{}, Limits{MaxMessageBytes: 128}, -1},
		{"local-error", [][]any{{"a"}, {"b"}, {strings.Repeat("x", 200)}, {"c"}, {"d"}}, [][]any{{"a", "b"}, {"c", "d"}}, BatchOptions{}, Limits{MaxMessageBytes: 128}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := batchRewriteDB(t, Config{Mode: ModeOracle, Limits: tc.limits}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveBatchArrayBegin(ctx, t, p) {
					return
				}
				for _, values := range tc.chunks {
					if !readCommand(ctx, t, p, batchArrayPacket(t, values, wire.ExecuteOptions{})) || !sendPackets(ctx, t, p, commandPacket(uint64(len(values)), 0, 5)) {
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
			tc.opts.Strategy, tc.opts.ContinueOnError = BatchArrayBinding, true
			r, err := ExecBatch(context.Background(), tx, testBatchSQL, tc.rows, tc.opts)
			if r.Strategy != BatchArrayBinding || (err != nil) != (tc.bad >= 0) {
				t.Fatalf("chunks: %+v %v", r, err)
			}
			for i, item := range r.Items {
				if i == tc.bad {
					if item.Status != BatchFailed || item.Started || !errors.Is(item.Err, wire.ErrMessageTooLarge) {
						t.Fatalf("local input: %+v", item)
					}
				} else if item.Status != BatchSucceeded || !item.Started {
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
			cfg := Config{Mode: ModeOracle}
			if reason == testClientStrategy {
				cfg.PrepareMode = PrepareClient
			}
			db := batchRewriteDB(t, cfg, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				serveBatchArrayFallback(ctx, t, p, reason, status)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			if err := pinned.Raw(func(raw any) error {
				switch reason {
				case "array-unknown-version":
					raw.(*conn).session.obVersion = "unknown"
				case "array-old-version":
					raw.(*conn).session.obVersion = "4.2.0"
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var exec Execer = pinned
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
			r, err := ExecBatch(context.Background(), exec, testBatchSQL, rows, BatchOptions{Strategy: BatchArrayBinding, ContinueOnError: reason != "array-stop"})
			if err != nil || r.Strategy != BatchSequential || batchSucceeded(&r) != 2 || reason == arrayFallbackValuer && calls != 2 {
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
		access := testOracleReadWrite
		if reason == arrayFallbackReadOnly {
			access = transactionOracleReadOnly
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {access, 5}}) {
			return
		}
	}
	for i := 1; i <= 2; i++ {
		if reason == testClientStrategy {
			if !readQuery(ctx, t, p, fmt.Sprintf("INSERT INTO t VALUES ( %d )", i)) {
				return
			}
		} else {
			if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
				return
			}
			payload := batchExecutePacket(t, int64(i))
			if reason == arrayFallbackValuer {
				payload = batchExecutePacket(t, 42)
			}
			if reason == arrayFallbackUnsigned {
				payload[13] = 128
			}
			if !readCommand(ctx, t, p, payload) {
				return
			}
		}
		if !sendPackets(ctx, t, p, commandPacket(1, 0, status)) || reason != testClientStrategy && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
	}
	if reason != arrayFallbackAutocommit && !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
		return
	}
	if !serveOK(ctx, t, p, []byte{14}, 6) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func TestBatchArrayCancellation(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := batchRewriteDB(t, Config{Mode: ModeOracle}, testStandard, func(peerCtx context.Context, p *wire.PacketIO) {
				if !serveBatchArrayBegin(peerCtx, t, p) || !readCommand(peerCtx, t, p, batchArrayPacket(t, []any{1, 2}, wire.ExecuteOptions{})) {
					return
				}
				if acknowledged {
					if !serveBatchArrayDiagnostics(peerCtx, t, p, batchArrayDiagnostic(1, 1062, "duplicate")) || !sendPackets(peerCtx, t, p, commandPacket(17, 0, 5)) {
						return
					}
				} else {
					cancel()
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(peerCtx); err == nil {
					t.Error("canceled array batch sent another command")
				}
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			if acknowledged {
				if err := pinned.Raw(func(raw any) error {
					c := raw.(*conn)
					return c.session.packets.ReplaceConn(&batchCancelReadConn{Conn: c.session.raw, response: commandPacket(17, 0, 5), cancel: cancel})
				}); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := pinned.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			r, err := ExecBatch(ctx, tx, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: BatchArrayBinding, ContinueOnError: true, ChunkRows: 2})
			if !errors.Is(err, context.Canceled) || r.Items[2].Status != BatchNotExecuted || r.Items[2].Started {
				t.Fatalf("cancellation: %+v %v", r, err)
			}
			if acknowledged {
				var server *Error
				if r.Items[0].Status != BatchSucceeded || r.Items[1].Status != BatchFailed || !errors.As(err, &server) || server.Code != 1062 {
					t.Fatalf("acknowledged error lost: %+v %v", r, err)
				}
			} else if r.Items[0].Status != BatchUnknown || r.Items[1].Status != BatchUnknown {
				t.Fatalf("unacknowledged cancellation: %+v %v", r, err)
			}
		})
	}
}

func TestBatchArrayResponseBudget(t *testing.T) {
	for _, deprecated := range []bool{false, true} {
		for _, oversize := range []bool{false, true} {
			t.Run(fmt.Sprint(deprecated, "/", oversize), func(t *testing.T) {
				packets := append(batchArrayDiagnostics(batchArrayDiagnostic(1, 1062, "duplicate")), commandPacket(1, 0, 5))
				var size int64
				for _, packet := range packets {
					size += int64(len(packet))
				}
				capabilities := uint64(0)
				if deprecated {
					capabilities = wire.ClientDeprecateEOF
				}
				c := outputTestConn(t, capabilities, func(ctx context.Context, p *wire.PacketIO) {
					if !readCommand(ctx, t, p, batchArrayPacket(t, []any{1, 2}, wire.ExecuteOptions{})) {
						return
					}
					for _, packet := range packets {
						if p.WriteMessage(ctx, packet) != nil {
							return
						}
					}
				})
				defer c.discard()
				c.cfg.Limits.MaxMessageBytes = size
				if oversize {
					c.cfg.Limits.MaxMessageBytes--
				}
				items := make([]BatchItem, 2)
				err := c.runBatchArrayLocked(context.Background(), batchArrayPacket(t, []any{1, 2}, wire.ExecuteOptions{}), items)
				if oversize {
					if !errors.Is(err, wire.ErrMessageTooLarge) || items[0].Status != BatchUnknown || items[1].Status != BatchUnknown || c.IsValid() {
						t.Fatalf("response budget: %+v %v", items, err)
					}
				} else if err != nil || items[0].Status != BatchSucceeded || items[1].Status != BatchFailed {
					t.Fatalf("complete native response: %+v %v", items, err)
				}
			})
		}
	}
}

func TestBatchArrayPhysicalParameterBudget(t *testing.T) {
	const query = "INSERT INTO t VALUES (:value,:value)"
	db := batchRewriteDB(t, Config{Mode: ModeOracle}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !servePrepareStatus(ctx, t, p, "INSERT INTO t VALUES (?,?)", 7, 2, nil, 5) {
			return
		}
		for _, value := range []uint64{1, 2} {
			parameter := wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, value)}
			payload, err := wire.EncodeStmtExecuteArray(7, [][]wire.Parameter{{parameter, parameter}}, 4096, wire.ExecuteOptions{})
			if err != nil || !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, commandPacket(1, 0, 5)) {
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
	r, err := ExecBatch(context.Background(), tx, query, [][]any{{sql.Named("value", 1)}, {sql.Named("value", 2)}}, BatchOptions{Strategy: BatchArrayBinding, ContinueOnError: true, ChunkParameters: 3})
	if err != nil || r.Strategy != BatchArrayBinding || batchSucceeded(&r) != 2 || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || !r.Items[1].RowsAffectedKnown {
		t.Fatalf("expanded physical slots: %+v %v", r, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
