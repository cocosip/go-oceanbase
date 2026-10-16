package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testMixedSQL = "UPDATE t SET v=1; SELECT v; UPDATE t SET v=2"

func commandPacket(affected, id uint64, status byte) []byte {
	data := binary.LittleEndian.AppendUint64([]byte{254}, affected)
	data = binary.LittleEndian.AppendUint64(append(data, 254), id)
	return append(append([]byte{0}, data...), status, 0, 0, 0)
}

func pinnedSession(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestExecutionOrder(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, testMixedSQL) {
			return
		}
		if !sendPackets(ctx, t, p, commandPacket(0, 0, 10), []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, commandPacket(2, math.MaxUint64, 2)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	e, err := Execute(context.Background(), c, testMixedSQL)
	if err != nil {
		t.Fatal(err)
	}
	initial := e.Snapshot()
	assertInitialExecution(t, initial)
	initial.Results[0].RowsAffected = 99
	if e.Snapshot().Results[0].RowsAffected != 0 {
		t.Fatal("snapshot aliases internal storage")
	}
	r := e.Rows()
	if r == nil || e.Rows() != r {
		t.Fatal("Rows identity")
	}
	if !r.Next() {
		t.Fatal(r.Err())
	}
	var value int
	if err = r.Scan(&value); err != nil || value != 1 {
		t.Fatal(value, err)
	}
	if r.Next() || r.Err() != nil {
		t.Fatal(r.Err())
	}
	if r.NextResultSet() {
		t.Fatal("command exposed as fake rowset")
	}
	final := e.Snapshot()
	if !final.Done || final.Err != nil || len(final.Results) != 3 || !final.Results[1].Complete || final.Results[1].RowsAffectedKnown || final.Results[2].RowsAffected != 2 || final.Results[2].InsertID != testMaxUint64Text || !final.Results[2].InsertIDKnown {
		t.Fatal(final)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertInitialExecution(t *testing.T, s ExecutionSnapshot) {
	t.Helper()
	if s.Done || len(s.Results) != 2 || s.Results[0].Kind != ResultCommand || !s.Results[0].RowsAffectedKnown || s.Results[0].RowsAffected != 0 || s.Results[0].InsertIDKnown || s.Results[0].RowSetIndex != -1 || s.Results[1].RowSetIndex != 0 || s.Results[1].Complete {
		t.Fatal(s)
	}
}

func TestExecutionPartialError(t *testing.T) {
	for _, duringRows := range []bool{false, true} {
		t.Run(map[bool]string{false: "query-error", true: "row-error"}[duringRows], func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, testMixedSQL) {
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(1, 0, 10)) {
					return
				}
				if duringRows && !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}) {
					return
				}
				if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c := pinnedSession(t, db)
			e, err := Execute(context.Background(), c, testMixedSQL)
			if e == nil {
				t.Fatal("lost execution prefix", err)
			}
			if duringRows {
				if err != nil || e.Rows() == nil {
					t.Fatal(err)
				}
				if e.Rows().Next() {
					t.Fatal("unexpected row")
				}
				err = e.Rows().Err()
			} else if e.Rows() != nil {
				t.Fatal("failed Query returned Rows")
			}
			var server *Error
			if !errors.As(err, &server) {
				t.Fatal(err)
			}
			s := e.Snapshot()
			if !s.Done || s.Err == nil || len(s.Results) != 2 || s.Results[0].RowsAffected != 1 {
				t.Fatal(s)
			}
			if duringRows {
				if s.Results[1].Kind != ResultRows || s.Results[1].Complete || s.Results[1].Err == nil {
					t.Fatal(s)
				}
			} else if s.Results[1].Kind != ResultFailure || !s.Results[1].Complete {
				t.Fatal(s)
			}
			if err = e.Close(); !errors.As(err, &server) {
				t.Fatal("close lost earlier error", err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionClose(t *testing.T) {
	ready := make(chan struct{})
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, selectBindSQL) || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}) {
			return
		}
		<-ready
		if !sendPackets(ctx, t, p, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, commandPacket(7, 0, 2)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	e, err := Execute(context.Background(), c, selectBindSQL)
	if err != nil {
		close(ready)
		t.Fatal(err)
	}
	for range 10 {
		if e.Snapshot().Done {
			close(ready)
			t.Fatal("Snapshot drove network")
		}
	}
	close(ready)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			_ = e.Snapshot()
		}
	}()
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot()
	if !s.Done || s.Err != nil || len(s.Results) != 2 || !s.Results[0].Complete || s.Results[1].RowsAffected != 7 {
		t.Fatal(s)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionRejectSession(t *testing.T) {
	for _, session := range []Session{nil, (*sql.Conn)(nil), (*sql.Tx)(nil), (*sql.DB)(nil)} {
		if e, err := Execute(context.Background(), session, "SELECT 1"); e != nil || err == nil {
			t.Fatal("invalid session accepted")
		}
	}
}

func TestExecutionLimits(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "items", true: "count"}[overflow], func(t *testing.T) {
			connector, err := NewConnector(Config{User: testUser, Limits: Limits{MaxResultItems: 1}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !readQuery(ctx, t, p, testMixedSQL) {
					return
				}
				if overflow {
					_ = sendPackets(ctx, t, p, commandPacket(math.MaxUint64, 0, 2))
					p.ResetSequence()
					_, _ = p.ReadMessage(ctx)
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(1, 0, 10), []byte{1}) {
					return
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("unsynchronized result limit session reused")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			c := pinnedSession(t, db)
			e, err := Execute(context.Background(), c, testMixedSQL)
			if err == nil || e == nil {
				t.Fatal("missing failed execution", err)
			}
			s := e.Snapshot()
			if !s.Done || s.Err == nil || len(s.Results) != 1 {
				t.Fatal(s)
			}
			if overflow && (!errors.Is(err, errCountOverflow) || s.Results[0].RowsAffectedKnown || !s.Results[0].Complete) {
				t.Fatal(s, err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionCloseFailure(t *testing.T) {
	closePeer := make(chan struct{})
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		// Lost response after metadata: the row result remains incomplete.
		<-closePeer
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	e, err := Execute(context.Background(), c, "SELECT v")
	if err != nil {
		close(closePeer)
		t.Fatal(err)
	}
	close(closePeer)
	if err = e.Rows().Close(); err == nil {
		t.Fatal("lost close error")
	}
	s := e.Snapshot()
	if !s.Done || s.Err == nil || s.Results[0].Complete || s.Results[0].Err == nil {
		t.Fatal(s)
	}
	if err = e.Close(); err == nil {
		t.Fatal("execution lost auto-close failure")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionCancellation(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := Execute(ctx, c, "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for !e.Snapshot().Done && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := e.Snapshot()
	if !s.Done || !errors.Is(s.Err, context.Canceled) || s.Results[0].Complete {
		t.Fatal(s)
	}
	if err = e.Close(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionParameters(t *testing.T) {
	for _, strategy := range []PrepareMode{PrepareServer, PrepareClient} {
		t.Run(map[PrepareMode]string{PrepareServer: testServerStrategy, PrepareClient: testClientStrategy}[strategy], func(t *testing.T) {
			connector, err := NewConnector(Config{User: testUser, PrepareMode: strategy, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if strategy == PrepareServer {
					if !servePrepare(ctx, t, p, selectBindSQL, 1, 1, nil) {
						return
					}
					payload := binary.LittleEndian.AppendUint64(append(executeHeader(1), 0, 1, 8, 0), 1)
					if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) || !readCommand(ctx, t, p, stmtPacket(25, 1)) {
						return
					}
				} else if !readQuery(ctx, t, p, "SELECT  1 ") || !sendPackets(ctx, t, p, commandPacket(1, 0, 2)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			c := pinnedSession(t, db)
			e, err := Execute(context.Background(), c, selectBindSQL, int64(1))
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			if s := e.Snapshot(); !s.Done || s.Err != nil || len(s.Results) != 1 || s.Results[0].RowsAffected != 1 {
				t.Fatal(s)
			}
			if e, err = Execute(context.Background(), c, selectBindSQL, make(chan int)); e != nil || err == nil {
				t.Fatal("pre-send error reported sent execution", err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionTx(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveOK(ctx, t, p, []byte("\x03UPDATE t SET v=1"), 3) || !serveTxSteps(ctx, t, p, []txStep{{testCommitSQL, 2}}) {
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
	e, err := Execute(context.Background(), tx, "UPDATE t SET v=1")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionTxCancellation(t *testing.T) {
	sendRows := make(chan struct{})
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}) {
			return
		}
		<-sendRows
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		close(sendRows)
		t.Fatal(err)
	}
	e, err := Execute(context.Background(), tx, "SELECT v")
	if err != nil {
		close(sendRows)
		t.Fatal(err)
	}
	cancel()
	close(sendRows)
	deadline := time.Now().Add(time.Second)
	for !e.Snapshot().Done && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := e.Snapshot()
	if !s.Done || !errors.Is(s.Err, context.Canceled) {
		t.Fatal("transaction cancellation reported success", s, e.Rows().Err())
	}
	if err = e.Close(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExecutionTxFinalization(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			command := "ROLLBACK"
			if commit {
				command = testCommitSQL
			}
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}, []byte{1, '1'}, []byte{254, 0, 0, 3, 0}) || !serveTxSteps(ctx, t, p, []txStep{{command, 2}}) {
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
			e, err := Execute(context.Background(), tx, "SELECT v")
			if err != nil {
				t.Fatal(err)
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(e.Rows().Err(), context.Canceled) {
				t.Fatal("fixture did not exercise SQL transaction row cancellation", e.Rows().Err())
			}
			if err = e.Close(); !errors.Is(err, context.Canceled) {
				t.Fatal("SQL row cancellation missing from close", err)
			}
			if s := e.Snapshot(); !s.Done || !errors.Is(s.Err, context.Canceled) {
				t.Fatal("SQL row cancellation missing from final snapshot", s)
			}
		})
	}
}

func TestExecutionTxDeadline(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Execute(context.Background(), tx, "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if err = e.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost transaction deadline cause", err)
	}
	if s := e.Snapshot(); !s.Done || !errors.Is(s.Err, context.DeadlineExceeded) {
		t.Fatal(s)
	}
}

func TestExecutionPreserveResultError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	e, err := Execute(ctx, c, "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("application canceled execution")
	cancel(cause)
	deadline := time.Now().Add(time.Second)
	for !e.Snapshot().Done && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := e.Snapshot()
	if !s.Done || !errors.Is(s.Err, cause) || s.Results[0].Err == nil {
		t.Fatal(s)
	}
	if err = e.Close(); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if final := e.Snapshot(); final.Results[0].Err != s.Results[0].Err {
		t.Fatal("close replaced original per-result cause", s, final)
	}
}
