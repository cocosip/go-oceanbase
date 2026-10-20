package batch_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"io"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testBatchSQL = "INSERT INTO t VALUES (?)"

type batchExecer func(context.Context, string, ...any) (sql.Result, error)

func (f batchExecer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return f(ctx, q, args...)
}

func TestBatchResults(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "continue"}[keepGoing], func(t *testing.T) {
			server := &oceanbase.Error{Code: 1062, SQLState: "23000"}
			var seen []int
			exec := batchExecer(func(_ context.Context, q string, args ...any) (sql.Result, error) {
				if q != "INSERT INTO t VALUES (?)" {
					t.Fatal(q)
				}
				i := args[0].(int)
				seen = append(seen, i)
				if i == 2 {
					return nil, server
				}
				return command.Result{Affected: uint64(i), Known: i != 3}, nil
			})
			r, err := oceanbase.ExecBatch(context.Background(), exec, "INSERT INTO t VALUES (?)", [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{ContinueOnError: keepGoing, ChunkRows: 1})
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || !errors.Is(err, server) || be.Index != 1 || len(r.Items) != 3 {
				t.Fatalf("%+v %v", r, err)
			}
			if r.Items[0].Status != oceanbase.BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || r.Items[1].Status != oceanbase.BatchFailed {
				t.Fatal(r)
			}
			want := []int{1, 2}
			completed := 1
			if keepGoing {
				want = append(want, 3)
				completed = 2
				if r.Items[2].Status != oceanbase.BatchSucceeded || r.Items[2].RowsAffectedKnown {
					t.Fatal(r)
				}
			} else if r.Items[2].Status != oceanbase.BatchNotExecuted {
				t.Fatal(r)
			}
			if !reflect.DeepEqual(seen, want) || be.Completed != completed {
				t.Fatal(seen, be)
			}
		})
	}
}

func TestBatchAtomicity(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "continue"}[keepGoing], func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
					return
				}
				if !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) {
					return
				}
				if keepGoing && (!testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2))) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, "INSERT INTO t VALUES (1)", [][]any{nil, nil, nil}, oceanbase.BatchOptions{ContinueOnError: keepGoing, Strategy: oceanbase.BatchRewrite})
			if err == nil || len(r.Items) != 3 || r.Items[0].Status != oceanbase.BatchSucceeded || !r.Items[0].Started || r.Items[1].Status != oceanbase.BatchFailed || !r.Items[1].Started || r.Strategy != oceanbase.BatchSequential {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestBatchStopsOnUncertainError(t *testing.T) {
	calls := 0
	r, err := oceanbase.ExecBatch(context.Background(), batchExecer(func(context.Context, string, ...any) (sql.Result, error) { calls++; return nil, io.ErrUnexpectedEOF }), "UPDATE t SET v=?", [][]any{{1}, {2}}, oceanbase.BatchOptions{ContinueOnError: true})
	if !errors.Is(err, io.ErrUnexpectedEOF) || calls != 1 || len(r.Items) != 2 || r.Items[0].Status != oceanbase.BatchUnknown || r.Items[1].Status != oceanbase.BatchNotExecuted {
		t.Fatal(r, err, calls)
	}
}

func TestBatchValidation(t *testing.T) {
	calls := 0
	exec := batchExecer(func(context.Context, string, ...any) (sql.Result, error) {
		calls++
		return command.Result{Known: true}, nil
	})
	for _, opts := range []oceanbase.BatchOptions{{ChunkRows: -1}, {ChunkParameters: -1}, {Strategy: 255}} {
		if _, err := oceanbase.ExecBatch(context.Background(), exec, "UPDATE t SET v=?", [][]any{{1}}, opts); err == nil {
			t.Fatal(opts)
		}
	}
	if _, err := oceanbase.ExecBatch(context.Background(), exec, "UPDATE t SET v=?", nil, oceanbase.BatchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := oceanbase.ExecBatch(context.Background(), exec, "UPDATE t SET v=?", [][]any{{1, 2}}, oceanbase.BatchOptions{ChunkParameters: 1}); err == nil {
		t.Fatal("parameter limit")
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestBatchNilExecer(t *testing.T) {
	var function batchExecer
	var db *sql.DB
	for _, exec := range []oceanbase.Execer{nil, function, db} {
		if _, err := oceanbase.ExecBatch(context.Background(), exec, testBatchSQL, [][]any{{1}}, oceanbase.BatchOptions{}); err == nil {
			t.Fatal("nil Execer accepted")
		}
	}
}

func batchExecutePacket(t *testing.T, value int64) []byte {
	t.Helper()
	p, err := valuecodec.EncodeParameter(value, oceanbase.ModeMySQL, oceanbase.SessionOptions{}, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := wire.EncodeStmtExecute(7, []wire.Parameter{p}, 128)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchStrategies(t *testing.T) {
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchAuto, oceanbase.BatchMultiSend, oceanbase.BatchSequential, oceanbase.BatchRewrite, oceanbase.BatchBulk, oceanbase.BatchArrayBinding} {
		t.Run(string(rune('a'+strategy)), func(t *testing.T) {
			multi := strategy == oceanbase.BatchAuto || strategy == oceanbase.BatchMultiSend
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				for i := 0; i < 3; i++ {
					if (i == 0 || !multi) && !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
						return
					}
					if !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, int64(i+1))) {
						return
					}
					response := publictest.CommandPacket(1, 17, 2)
					if i == 1 {
						response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if i == 2 {
						response = publictest.CommandPacket(math.MaxUint64, math.MaxUint64, 2)
					}
					if !testpeer.SendPackets(ctx, t, p, response) {
						return
					}
					if !multi && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
				}
				if multi && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkRows: 2, ChunkParameters: 2})
			want := oceanbase.BatchSequential
			if multi {
				want = oceanbase.BatchMultiSend
			}
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || be.Index != 1 || be.Completed != 2 || r.Strategy != want || len(r.Items) != 3 {
				t.Fatal(r, err)
			}
			if r.Items[0].InsertID != "17" || !r.Items[0].InsertIDKnown || r.Items[1].InsertIDKnown || r.Items[2].InsertID != testMaxUint64Text || !r.Items[2].InsertIDKnown || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchFailed || r.Items[2].Status != oceanbase.BatchSucceeded || r.Items[2].RowsAffectedKnown {
				t.Fatal(r)
			}
		})
	}
}

func TestBatchTxBoundary(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
			t.Run(fmt.Sprint(mode, "/", strategy), func(t *testing.T) {
				multi := strategy == oceanbase.BatchMultiSend
				db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
					setup := []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}
					active := byte(3)
					if mode == oceanbase.ModeOracle {
						setup = []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}
						active = 5
					}
					if !publictest.ServeTxSteps(ctx, t, p, setup) {
						return
					}
					for i := 0; i < 3; i++ {
						if (i == 0 || !multi) && !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, active) {
							return
						}
						if !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, int64(i+1))) {
							return
						}
						response := publictest.CommandPacket(1, 0, active)
						if i == 1 {
							response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
						}
						if !testpeer.SendPackets(ctx, t, p, response) {
							return
						}
						if !multi && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
							return
						}
					}
					if multi && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
					ending := []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}
					if mode == oceanbase.ModeOracle {
						ending = []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}
					}
					if !publictest.ServeTxSteps(ctx, t, p, ending) {
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
				r, err := oceanbase.ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
				if err == nil || len(r.Items) != 3 || r.Items[2].Status != oceanbase.BatchSucceeded {
					t.Fatal(r, err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestBatchLongAndOutputs(t *testing.T) {
	const query = "BEGIN proc(?,?); END;"
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		for n := uint64(42); n < 44; n++ {
			if !publictest.ServePrepare(ctx, t, p, query, 79, 2, nil) || !testpeer.ReadCommand(ctx, t, p, longDataPacket(oceanbase.ModeOracle, 79, 0, 3, []byte("6162"))) || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return
			}
			payload, err := wire.EncodePrepareExecute(79, []byte(query), []wire.Parameter{{Type: 254, LongData: true}, {Type: 6}}, 1<<20)
			if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) {
				t.Error(err)
				return
			}
			if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(79, 1, 2, 4), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, n), []byte{254, 0, 0, 14, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 79)) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var first, second, third int64
	stream := &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}
	rows := [][]any{{stream, sql.Out{Dest: &first}}, {stream, sql.Out{Dest: &second}}, {&oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &third}}}
	r, err := oceanbase.ExecBatch(context.Background(), db, query, rows, oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiSend, ContinueOnError: true})
	if !errors.Is(err, oceanbase.ErrLongData) || len(r.Items) != 3 || r.Strategy != oceanbase.BatchSequential || first != 42 || second != 0 || third != 43 || r.Items[1].Started || r.Items[2].Status != oceanbase.BatchSucceeded {
		t.Fatal(r, err, first, second, third)
	}
}

func TestBatchDisconnectNoReplay(t *testing.T) {
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			var dials atomic.Int32
			dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 1)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
					return
				}
				if strategy == oceanbase.BatchSequential {
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) || !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
						return
					}
				}
				_ = testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 2))
			})
			connector, err := oceanbase.NewConnector(oceanbase.Config{User: testpeer.User, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return dial(ctx, network, addr)
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkRows: 2})
			if err == nil || dials.Load() != 1 || len(r.Items) != 3 || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchUnknown || r.Items[2].Status != oceanbase.BatchNotExecuted {
				t.Fatal(r, err, dials.Load())
			}
		})
	}
}

func TestBatchCancellation(t *testing.T) {
	for _, cancelTx := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelTx), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := publictest.QueryDB(t, func(peerctx context.Context, p *wire.PacketIO) {
				status := byte(2)
				if cancelTx {
					if !publictest.ServeTxSteps(peerctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) {
						return
					}
					status = 3
				}
				if !publictest.ServePrepareStatus(peerctx, t, p, testBatchSQL, 7, 1, nil, status) || !testpeer.ReadCommand(peerctx, t, p, batchExecutePacket(t, 1)) {
					return
				}
				cancel()
				p.ResetSequence()
				if _, err := p.ReadMessage(peerctx); err == nil {
					t.Error("canceled batch kept transport")
				}
			})
			defer func() { _ = db.Close() }()
			var exec oceanbase.Execer = db
			batchctx := ctx
			if cancelTx {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				exec, batchctx = tx, context.Background()
			}
			r, err := oceanbase.ExecBatch(batchctx, exec, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{ContinueOnError: true, ChunkRows: 1})
			if err == nil || len(r.Items) != 2 || r.Items[0].Status != oceanbase.BatchUnknown || r.Items[1].Status != oceanbase.BatchNotExecuted {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchMalformedResponse(t *testing.T) {
	for _, response := range [][]byte{{1}, {255}, publictest.CommandPacket(1, 0, 10)} {
		t.Run(fmt.Sprintf("%x", response), func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 1)) || !testpeer.SendPackets(ctx, t, p, response) {
					return
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("malformed batch session retained")
				}
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := oceanbase.ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{ContinueOnError: true, ChunkRows: 1})
			if !errors.Is(err, wire.ErrMalformed) || len(r.Items) != 2 || r.Items[0].Status != oceanbase.BatchUnknown || r.Items[1].Status != oceanbase.BatchNotExecuted {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchAcknowledgedBeforeCleanupError(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 1)) {
			return
		}
		_ = testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, math.MaxUint64, 2))
	})
	defer func() { _ = db.Close() }()
	r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchSequential})
	checkBatchInsertID(t, r.Items[0], testMaxUint64Text)
	var be *oceanbase.BatchError
	if !errors.As(err, &be) || be.Index != 0 || be.Completed != 1 || len(r.Items) != 2 || r.Items[0].Status != oceanbase.BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || r.Items[0].Err == nil || r.Items[1].Status != oceanbase.BatchNotExecuted {
		t.Fatal(r, err)
	}
}

func TestBatchPrepareFailureContinues(t *testing.T) {
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, testBatchSQL...)) || !testpeer.SendPackets(ctx, t, p, []byte{255, 122, 4, '#', '4', '2', 'S', '0', '2', 'm', 'i', 's', 's', 'i', 'n', 'g'}) {
					return
				}
				for i := int64(2); i <= 3; i++ {
					if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, i)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || be.Index != 0 || be.Completed != 2 || r.Strategy != oceanbase.BatchSequential || r.Items[0].Started || r.Items[0].Status != oceanbase.BatchFailed || r.Items[1].Status != oceanbase.BatchSucceeded || r.Items[2].Status != oceanbase.BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchPrepareValidationOrder(t *testing.T) {
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, testBatchSQL...)) || !testpeer.SendPackets(ctx, t, p, []byte{255, 122, 4, '#', '4', '2', 'S', '0', '2', 'm', 'i', 's', 's', 'i', 'n', 'g'}) {
					return
				}
				if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 3)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, testBatchSQL, [][]any{{sql.Named("1bad", 1)}, {2}, {3}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
			var be *oceanbase.BatchError
			var server *oceanbase.Error
			if !errors.As(err, &be) || be.Index != 0 || be.Completed != 1 || !errors.Is(r.Items[0].Err, oceanbase.ErrBind) || !errors.As(r.Items[1].Err, &server) || r.Items[0].Started || r.Items[1].Started || r.Items[2].Status != oceanbase.BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchNamedChunkLimit(t *testing.T) {
	const query = "INSERT INTO t VALUES (:v,:v)"
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				for i := int64(1); i <= 2; i++ {
					param, err := valuecodec.EncodeParameter(i, oceanbase.ModeOracle, oceanbase.SessionOptions{}, 64)
					if err != nil {
						t.Error(err)
						return
					}
					command, err := wire.EncodeStmtExecute(7, []wire.Parameter{param, param}, 128)
					if err != nil || !publictest.ServePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 7, 2, nil) || !testpeer.ReadCommand(ctx, t, p, command) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						t.Error(err)
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := oceanbase.ExecBatch(context.Background(), db, query, [][]any{{sql.Named("v", 1)}, {sql.Named("v", 2)}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkParameters: 1})
			if err != nil || r.Strategy != oceanbase.BatchSequential || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}
