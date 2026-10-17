package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testBatchSQL = "INSERT INTO t VALUES (?)"

type batchExecer func(context.Context, string, ...any) (sql.Result, error)

func (f batchExecer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return f(ctx, q, args...)
}

func TestBatchResults(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "continue"}[keepGoing], func(t *testing.T) {
			server := &Error{Code: 1062, SQLState: "23000"}
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
				return result{affected: uint64(i), known: i != 3}, nil
			})
			r, err := ExecBatch(context.Background(), exec, "INSERT INTO t VALUES (?)", [][]any{{1}, {2}, {3}}, BatchOptions{ContinueOnError: keepGoing, ChunkRows: 1})
			var be *BatchError
			if !errors.As(err, &be) || !errors.Is(err, server) || be.Index != 1 || len(r.Items) != 3 {
				t.Fatalf("%+v %v", r, err)
			}
			if r.Items[0].Status != BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || r.Items[1].Status != BatchFailed {
				t.Fatal(r)
			}
			want := []int{1, 2}
			completed := 1
			if keepGoing {
				want = append(want, 3)
				completed = 2
				if r.Items[2].Status != BatchSucceeded || r.Items[2].RowsAffectedKnown {
					t.Fatal(r)
				}
			} else if r.Items[2].Status != BatchNotExecuted {
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
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
					return
				}
				if !readQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) {
					return
				}
				if keepGoing && (!readQuery(ctx, t, p, "INSERT INTO t VALUES (1)") || !sendPackets(ctx, t, p, commandPacket(1, 0, 2))) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, "INSERT INTO t VALUES (1)", [][]any{nil, nil, nil}, BatchOptions{ContinueOnError: keepGoing, Strategy: BatchRewrite})
			if err == nil || len(r.Items) != 3 || r.Items[0].Status != BatchSucceeded || !r.Items[0].Started || r.Items[1].Status != BatchFailed || !r.Items[1].Started || r.Strategy != BatchSequential {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestBatchStopsOnUncertainError(t *testing.T) {
	calls := 0
	r, err := ExecBatch(context.Background(), batchExecer(func(context.Context, string, ...any) (sql.Result, error) { calls++; return nil, io.ErrUnexpectedEOF }), "UPDATE t SET v=?", [][]any{{1}, {2}}, BatchOptions{ContinueOnError: true})
	if !errors.Is(err, io.ErrUnexpectedEOF) || calls != 1 || len(r.Items) != 2 || r.Items[0].Status != BatchUnknown || r.Items[1].Status != BatchNotExecuted {
		t.Fatal(r, err, calls)
	}
}

func TestBatchValidation(t *testing.T) {
	calls := 0
	exec := batchExecer(func(context.Context, string, ...any) (sql.Result, error) { calls++; return result{known: true}, nil })
	for _, opts := range []BatchOptions{{ChunkRows: -1}, {ChunkParameters: -1}, {Strategy: 255}} {
		if _, err := ExecBatch(context.Background(), exec, "UPDATE t SET v=?", [][]any{{1}}, opts); err == nil {
			t.Fatal(opts)
		}
	}
	if _, err := ExecBatch(context.Background(), exec, "UPDATE t SET v=?", nil, BatchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecBatch(context.Background(), exec, "UPDATE t SET v=?", [][]any{{1, 2}}, BatchOptions{ChunkParameters: 1}); err == nil {
		t.Fatal("parameter limit")
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestBatchNilExecer(t *testing.T) {
	var function batchExecer
	var db *sql.DB
	for _, exec := range []Execer{nil, function, db} {
		if _, err := ExecBatch(context.Background(), exec, testBatchSQL, [][]any{{1}}, BatchOptions{}); err == nil {
			t.Fatal("nil Execer accepted")
		}
	}
}

func batchExecutePacket(t *testing.T, value int64) []byte {
	t.Helper()
	p, err := encodeParameter(value, ModeMySQL, SessionOptions{}, 64)
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
	for _, strategy := range []BatchStrategy{BatchAuto, BatchMultiSend, BatchSequential, BatchRewrite, BatchBulk, BatchArrayBinding} {
		t.Run(string(rune('a'+strategy)), func(t *testing.T) {
			multi := strategy == BatchAuto || strategy == BatchMultiSend
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				for i := 0; i < 3; i++ {
					if (i == 0 || !multi) && !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
						return
					}
					if !readCommand(ctx, t, p, batchExecutePacket(t, int64(i+1))) {
						return
					}
					response := commandPacket(1, 0, 2)
					if i == 1 {
						response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
					}
					if i == 2 {
						response = commandPacket(math.MaxUint64, 0, 2)
					}
					if !sendPackets(ctx, t, p, response) {
						return
					}
					if !multi && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						return
					}
				}
				if multi && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkRows: 2, ChunkParameters: 2})
			want := BatchSequential
			if multi {
				want = BatchMultiSend
			}
			var be *BatchError
			if !errors.As(err, &be) || be.Index != 1 || be.Completed != 2 || r.Strategy != want || len(r.Items) != 3 {
				t.Fatal(r, err)
			}
			if r.Items[0].Status != BatchSucceeded || r.Items[1].Status != BatchFailed || r.Items[2].Status != BatchSucceeded || r.Items[2].RowsAffectedKnown {
				t.Fatal(r)
			}
		})
	}
}

func TestBatchTxBoundary(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
			t.Run(fmt.Sprint(mode, "/", strategy), func(t *testing.T) {
				multi := strategy == BatchMultiSend
				db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
					setup := []txStep{{testMySQLReadWrite, 3}}
					active := byte(3)
					if mode == ModeOracle {
						setup = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
						active = 5
					}
					if !serveTxSteps(ctx, t, p, setup) {
						return
					}
					for i := 0; i < 3; i++ {
						if (i == 0 || !multi) && !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, active) {
							return
						}
						if !readCommand(ctx, t, p, batchExecutePacket(t, int64(i+1))) {
							return
						}
						response := commandPacket(1, 0, active)
						if i == 1 {
							response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
						}
						if !sendPackets(ctx, t, p, response) {
							return
						}
						if !multi && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
							return
						}
					}
					if multi && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						return
					}
					ending := []txStep{{testRollbackSQL, 2}}
					if mode == ModeOracle {
						ending = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
					}
					if !serveTxSteps(ctx, t, p, ending) {
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
				r, err := ExecBatch(context.Background(), tx, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true})
				if err == nil || len(r.Items) != 3 || r.Items[2].Status != BatchSucceeded {
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
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		for n := uint64(42); n < 44; n++ {
			if !servePrepare(ctx, t, p, query, 79, 2, nil) || !readCommand(ctx, t, p, longDataPacket(ModeOracle, 79, 0, 3, []byte("6162"))) || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
				return
			}
			payload, err := wire.EncodePrepareExecute(79, []byte(query), []wire.Parameter{{Type: 254, LongData: true}, {Type: 6}}, 1<<20)
			if err != nil || !readCommand(ctx, t, p, payload) {
				t.Error(err)
				return
			}
			if !sendPackets(ctx, t, p, combinedHeader(79, 1, 2, 4), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, n), []byte{254, 0, 0, 14, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 79)) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var first, second, third int64
	stream := &LongData{Reader: strings.NewReader("ab"), Length: -1}
	rows := [][]any{{stream, sql.Out{Dest: &first}}, {stream, sql.Out{Dest: &second}}, {&LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &third}}}
	r, err := ExecBatch(context.Background(), db, query, rows, BatchOptions{Strategy: BatchMultiSend, ContinueOnError: true})
	if !errors.Is(err, ErrLongData) || len(r.Items) != 3 || r.Strategy != BatchSequential || first != 42 || second != 0 || third != 43 || r.Items[1].Started || r.Items[2].Status != BatchSucceeded {
		t.Fatal(r, err, first, second, third)
	}
}

func TestBatchDisconnectNoReplay(t *testing.T) {
	for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			var dials atomic.Int32
			dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 1)) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
					return
				}
				if strategy == BatchSequential {
					if !readCommand(ctx, t, p, stmtPacket(25, 7)) || !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
						return
					}
				}
				_ = readCommand(ctx, t, p, batchExecutePacket(t, 2))
			})
			connector, err := NewConnector(Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return dial(ctx, network, addr)
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkRows: 2})
			if err == nil || dials.Load() != 1 || len(r.Items) != 3 || r.Items[0].Status != BatchSucceeded || r.Items[1].Status != BatchUnknown || r.Items[2].Status != BatchNotExecuted {
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
			db := queryDB(t, func(peerctx context.Context, p *wire.PacketIO) {
				status := byte(2)
				if cancelTx {
					if !serveTxSteps(peerctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
						return
					}
					status = 3
				}
				if !servePrepareStatus(peerctx, t, p, testBatchSQL, 7, 1, nil, status) || !readCommand(peerctx, t, p, batchExecutePacket(t, 1)) {
					return
				}
				cancel()
				p.ResetSequence()
				if _, err := p.ReadMessage(peerctx); err == nil {
					t.Error("canceled batch kept transport")
				}
			})
			defer func() { _ = db.Close() }()
			var exec Execer = db
			batchctx := ctx
			if cancelTx {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				exec, batchctx = tx, context.Background()
			}
			r, err := ExecBatch(batchctx, exec, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{ContinueOnError: true, ChunkRows: 1})
			if err == nil || len(r.Items) != 2 || r.Items[0].Status != BatchUnknown || r.Items[1].Status != BatchNotExecuted {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchMalformedResponse(t *testing.T) {
	for _, response := range [][]byte{{1}, {255}, commandPacket(1, 0, 10)} {
		t.Run(fmt.Sprintf("%x", response), func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 1)) || !sendPackets(ctx, t, p, response) {
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
			r, err := ExecBatch(ctx, db, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{ContinueOnError: true, ChunkRows: 1})
			if !errors.Is(err, wire.ErrMalformed) || len(r.Items) != 2 || r.Items[0].Status != BatchUnknown || r.Items[1].Status != BatchNotExecuted {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchSQLGate(t *testing.T) {
	for _, q := range []string{"SELECT ?", "CALL p(?)", "UPDATE t SET v=?; DELETE FROM t", "INSERT INTO t VALUES (?) RETURNING id INTO ?", "SET x=?", "INSERT /*! FROM */ INTO t VALUES (?)"} {
		if batchDML(q, ModeMySQL, false) {
			t.Fatal("unsafe pipeline SQL", q)
		}
	}
	for _, q := range []string{"INSERT INTO t VALUES (?)", "UPDATE t SET v=';' WHERE id=?", "/* ordinary */ DELETE FROM t WHERE id=?"} {
		if !batchDML(q, ModeMySQL, false) {
			t.Fatal("ordinary DML rejected", q)
		}
	}
}

func TestBatchAcknowledgedBeforeCleanupError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 1)) {
			return
		}
		_ = sendPackets(ctx, t, p, commandPacket(1, 0, 2))
	})
	defer func() { _ = db.Close() }()
	r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchSequential})
	var be *BatchError
	if !errors.As(err, &be) || be.Index != 0 || be.Completed != 1 || len(r.Items) != 2 || r.Items[0].Status != BatchSucceeded || !r.Items[0].RowsAffectedKnown || r.Items[0].RowsAffected != 1 || r.Items[0].Err == nil || r.Items[1].Status != BatchNotExecuted {
		t.Fatal(r, err)
	}
}

type batchCancelReadConn struct {
	net.Conn
	response []byte
	cancel   func()
}

func (c *batchCancelReadConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if bytes.Equal(b[:n], c.response) {
		c.cancel()
	}
	return n, err
}

func TestBatchCancellationPreservesPriorError(t *testing.T) {
	ctx := &longQueuedContext{Context: context.Background(), done: make(chan struct{})}
	c := outputTestConn(t, 0, func(peerctx context.Context, p *wire.PacketIO) {
		if !servePrepare(peerctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(peerctx, t, p, batchExecutePacket(t, 1)) || !sendPackets(peerctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}) || !readCommand(peerctx, t, p, batchExecutePacket(t, 2)) || !sendPackets(peerctx, t, p, commandPacket(17, 0, 2)) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(peerctx); err == nil {
			t.Error("canceled batch retained connection")
		}
	})
	c.session.mode = ModeMySQL
	raw := &batchCancelReadConn{Conn: c.session.conn, response: commandPacket(17, 0, 2), cancel: ctx.cancel}
	c.session.packets = wire.NewPacketIO(raw, 4096)
	r := BatchResult{Items: make([]BatchItem, 2), Strategy: BatchSequential}
	_, err := c.execBatch(ctx, batchRequest{query: testBatchSQL, rows: [][]any{{1}, {2}}, options: BatchOptions{ContinueOnError: true, ChunkRows: 1, ChunkParameters: 2}, result: &r})
	var server *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &server) || r.Items[1].Status != BatchSucceeded || c.IsValid() {
		t.Fatal(r, err, c.IsValid())
	}
}

type batchLateWriteConn struct {
	net.Conn
	command, response []byte
	read              chan struct{}
	err               error
}

func (c *batchLateWriteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if bytes.Equal(b[:n], c.response) {
		close(c.read)
	}
	return n, err
}

func (c *batchLateWriteConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err == nil && bytes.Equal(b, c.command) {
		<-c.read
		return n, c.err
	}
	return n, err
}

func TestBatchLateWritePreservesServerError(t *testing.T) {
	response := []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'd', 'u', 'p'}
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 1)) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, batchExecutePacket(t, 2)) {
			return
		}
		_ = sendPackets(ctx, t, p, response)
	})
	c.session.mode = ModeMySQL
	late := errors.New("late write failure")
	raw := &batchLateWriteConn{Conn: c.session.conn, command: batchExecutePacket(t, 2), response: response, read: make(chan struct{}), err: late}
	c.session.packets = wire.NewPacketIO(raw, 4096)
	r := BatchResult{Items: make([]BatchItem, 2), Strategy: BatchSequential}
	_, err := c.execBatch(context.Background(), batchRequest{query: testBatchSQL, rows: [][]any{{1}, {2}}, options: BatchOptions{ContinueOnError: true, ChunkRows: 2, ChunkParameters: 2}, result: &r})
	var server *Error
	if !errors.Is(err, late) || !errors.As(err, &server) || !errors.As(r.Items[1].Err, &server) || r.Items[1].Status != BatchFailed {
		t.Fatal(r, err)
	}
}

func TestBatchPrepareFailureContinues(t *testing.T) {
	for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, append([]byte{22}, testBatchSQL...)) || !sendPackets(ctx, t, p, []byte{255, 122, 4, '#', '4', '2', 'S', '0', '2', 'm', 'i', 's', 's', 'i', 'n', 'g'}) {
					return
				}
				for i := int64(2); i <= 3; i++ {
					if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, i)) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true})
			var be *BatchError
			if !errors.As(err, &be) || be.Index != 0 || be.Completed != 2 || r.Strategy != BatchSequential || r.Items[0].Started || r.Items[0].Status != BatchFailed || r.Items[1].Status != BatchSucceeded || r.Items[2].Status != BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchPrepareValidationOrder(t *testing.T) {
	for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, append([]byte{22}, testBatchSQL...)) || !sendPackets(ctx, t, p, []byte{255, 122, 4, '#', '4', '2', 'S', '0', '2', 'm', 'i', 's', 's', 'i', 'n', 'g'}) {
					return
				}
				if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 3)) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{sql.Named("1bad", 1)}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true})
			var be *BatchError
			var server *Error
			if !errors.As(err, &be) || be.Index != 0 || be.Completed != 1 || !errors.Is(r.Items[0].Err, ErrBind) || !errors.As(r.Items[1].Err, &server) || r.Items[0].Started || r.Items[1].Started || r.Items[2].Status != BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}

func TestBatchNamedChunkLimit(t *testing.T) {
	const query = "INSERT INTO t VALUES (:v,:v)"
	for _, strategy := range []BatchStrategy{BatchSequential, BatchMultiSend} {
		t.Run(fmt.Sprint(strategy), func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				for i := int64(1); i <= 2; i++ {
					param, err := encodeParameter(i, ModeOracle, SessionOptions{}, 64)
					if err != nil {
						t.Error(err)
						return
					}
					command, err := wire.EncodeStmtExecute(7, []wire.Parameter{param, param}, 128)
					if err != nil || !servePrepare(ctx, t, p, "INSERT INTO t VALUES (?,?)", 7, 2, nil) || !readCommand(ctx, t, p, command) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						t.Error(err)
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			r, err := ExecBatch(context.Background(), db, query, [][]any{{sql.Named("v", 1)}, {sql.Named("v", 2)}}, BatchOptions{Strategy: strategy, ContinueOnError: true, ChunkParameters: 1})
			if err != nil || r.Strategy != BatchSequential || r.Items[0].Status != BatchSucceeded || r.Items[1].Status != BatchSucceeded {
				t.Fatal(r, err)
			}
		})
	}
}
