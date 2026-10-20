package execution_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	math "math"
	sync "sync"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	command "github.com/cocosip/go-oceanbase/internal/command"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestExecutionPreserveResultError(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	e, err := oceanbase.Execute(ctx, c, "SELECT v")
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

func TestExecutionTxDeadline(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) || !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}) {
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
	e, err := oceanbase.Execute(context.Background(), tx, "SELECT v")
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

func TestExecutionTxFinalization(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			command := "ROLLBACK"
			if commit {
				command = testCommitSQL
			}
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) || !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}, []byte{1, '1'}, []byte{254, 0, 0, 3, 0}) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: command, Status: 2}}) {
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
			e, err := oceanbase.Execute(context.Background(), tx, "SELECT v")
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

func TestExecutionTxCancellation(t *testing.T) {
	sendRows := make(chan struct{})
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) || !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 3, 0}) {
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
	e, err := oceanbase.Execute(context.Background(), tx, "SELECT v")
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

func TestExecutionTx(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) || !testpeer.ServeOK(ctx, t, p, []byte("\x03UPDATE t SET v=1"), 3) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testCommitSQL, Status: 2}}) {
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
	e, err := oceanbase.Execute(context.Background(), tx, "UPDATE t SET v=1")
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

func TestExecutionParameters(t *testing.T) {
	for _, strategy := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		t.Run(map[oceanbase.PrepareMode]string{oceanbase.PrepareServer: testServerStrategy, oceanbase.PrepareClient: testClientStrategy}[strategy], func(t *testing.T) {
			connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, PrepareMode: strategy, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if strategy == oceanbase.PrepareServer {
					if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 1, 1, nil) {
						return
					}
					payload := binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(1), 0, 1, 8, 0), 1)
					if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 1)) {
						return
					}
				} else if !testpeer.ReadQuery(ctx, t, p, "SELECT  1 ") || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 2)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			c := publictest.PinnedSession(t, db)
			e, err := oceanbase.Execute(context.Background(), c, selectBindSQL, int64(1))
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			if s := e.Snapshot(); !s.Done || s.Err != nil || len(s.Results) != 1 || s.Results[0].RowsAffected != 1 {
				t.Fatal(s)
			}
			if e, err = oceanbase.Execute(context.Background(), c, selectBindSQL, make(chan int)); e != nil || err == nil {
				t.Fatal("pre-send error reported sent execution", err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionCancellation(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := oceanbase.Execute(ctx, c, "SELECT v")
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

func TestExecutionCloseFailure(t *testing.T) {
	closePeer := make(chan struct{})
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		// Lost response after metadata: the row result remains incomplete.
		<-closePeer
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	e, err := oceanbase.Execute(context.Background(), c, "SELECT v")
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

func TestExecutionLimits(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "items", true: "count"}[overflow], func(t *testing.T) {
			connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Limits: oceanbase.Limits{MaxResultItems: 1}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !testpeer.ReadQuery(ctx, t, p, testMixedSQL) {
					return
				}
				if overflow {
					_ = testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(math.MaxUint64, 0, 2))
					p.ResetSequence()
					_, _ = p.ReadMessage(ctx)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10), []byte{1}) {
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
			c := publictest.PinnedSession(t, db)
			e, err := oceanbase.Execute(context.Background(), c, testMixedSQL)
			if err == nil || e == nil {
				t.Fatal("missing failed execution", err)
			}
			s := e.Snapshot()
			if !s.Done || s.Err == nil || len(s.Results) != 1 {
				t.Fatal(s)
			}
			if overflow && (!errors.Is(err, command.ErrCountOverflow) || s.Results[0].RowsAffectedKnown || !s.Results[0].Complete) {
				t.Fatal(s, err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecutionRejectSession(t *testing.T) {
	for _, session := range []oceanbase.Session{nil, (*sql.Conn)(nil), (*sql.Tx)(nil), (*sql.DB)(nil)} {
		if e, err := oceanbase.Execute(context.Background(), session, "SELECT 1"); e != nil || err == nil {
			t.Fatal("invalid session accepted")
		}
	}
}

func TestExecutionClose(t *testing.T) {
	ready := make(chan struct{})
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, selectBindSQL) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}) {
			return
		}
		<-ready
		if !testpeer.SendPackets(ctx, t, p, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, publictest.CommandPacket(7, 0, 2)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	e, err := oceanbase.Execute(context.Background(), c, selectBindSQL)
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

func TestExecutionPartialError(t *testing.T) {
	for _, duringRows := range []bool{false, true} {
		t.Run(map[bool]string{false: "query-error", true: "row-error"}[duringRows], func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, testMixedSQL) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 10)) {
					return
				}
				if duringRows && !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			c := publictest.PinnedSession(t, db)
			e, err := oceanbase.Execute(context.Background(), c, testMixedSQL)
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
			var server *oceanbase.Error
			if !errors.As(err, &server) {
				t.Fatal(err)
			}
			s := e.Snapshot()
			if !s.Done || s.Err == nil || len(s.Results) != 2 || s.Results[0].RowsAffected != 1 {
				t.Fatal(s)
			}
			if duringRows {
				if s.Results[1].Kind != oceanbase.ResultRows || s.Results[1].Complete || s.Results[1].Err == nil {
					t.Fatal(s)
				}
				if len(s.Results[1].Columns) != 1 || s.Results[1].Columns[0].Name != "v" {
					t.Fatal("row error lost metadata", s)
				}
			} else if s.Results[1].Kind != oceanbase.ResultFailure || !s.Results[1].Complete {
				t.Fatal(s)
			} else if s.Results[1].Columns != nil {
				t.Fatal("failure has columns", s)
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

func TestExecutionOrder(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, testMixedSQL) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 10), []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, publictest.CommandPacket(2, math.MaxUint64, 2)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	e, err := oceanbase.Execute(context.Background(), c, testMixedSQL)
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
	if final.Results[0].Columns != nil || final.Results[2].Columns != nil || len(final.Results[1].Columns) != 1 || final.Results[1].Columns[0].Name != "v" {
		t.Fatal("command or row columns lost", final)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}
