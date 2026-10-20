package materialize_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	strings "strings"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorColumnFetchCancellation(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 128)) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("canceled child fetch reused physical connection")
		}
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stmt, err := s.PrepareContext(ctx, "SELECT child FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	fetchCtx, cancelFetch := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelFetch()
	if _, err = stmt.QueryContext(fetchCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("child fetch ignored context", err)
	}
}

func TestCursorColumnNested(t *testing.T) {
	for _, kind := range []string{testCursorNested, testCursorCycle, testCursorDepth} {
		t.Run(kind, func(t *testing.T) {
			count := 2
			if kind == testCursorCycle {
				count = 1
			}
			if kind == testCursorDepth {
				count = 16
			}
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) {
					return
				}
				for i := 0; i < count; i++ {
					typ, id := byte(163), uint32(92+i)
					value := binary.LittleEndian.AppendUint32([]byte{0, 0}, id+1)
					if kind == testCursorNested && i == count-1 {
						typ = 8
						value = binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
					}
					if kind == testCursorCycle {
						value = binary.LittleEndian.AppendUint32([]byte{0, 0}, id)
					}
					if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(id, 128)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", typ, 63, 0), []byte{254, 0, 0, 6, 0}, value, []byte{254, 0, 0, 134, 0}) {
						return
					}
				}
				for i := count - 1; i >= 0; i-- {
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, uint32(92+i))) {
						return
					}
				}
				p.ResetSequence()
				if kind != testCursorNested {
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("invalid nested cursor retained session")
					}
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			stmt, err := s.PrepareContext(ctx, "SELECT child FROM t")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stmt.Close() }()
			rows, err := stmt.QueryContext(ctx)
			if kind != testCursorNested {
				if !errors.Is(err, oceanbase.ErrCursor) {
					t.Fatal("nested cursor limit missing", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var child, grandchild sql.Rows
			var n int64
			if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&grandchild) != nil || !grandchild.Next() || grandchild.Scan(&n) != nil || n != 42 {
				t.Fatal("nested child rows", n, rows.Err(), child.Err(), grandchild.Err())
			}
		})
	}
}

func TestCursorColumnSharedMetadataBudget(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		row := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), 93)
		if !publictest.ServePrepare(ctx, t, p, "SELECT a,b FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) || !testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("a", 163, 63, 0), testpeer.TextColumn("b", 163, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) {
			return
		}
		col := testpeer.TextColumn("n", 8, 63, 0)
		col = append(wire.AppendComplexLenBytes(nil, []byte(strings.Repeat("c", 900))), col[4:]...)
		for _, id := range []uint32{92, 93} {
			if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(id, 128)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, id)) {
				return
			}
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	}, func(cfg *config.Config) { cfg.Limits.MaxMessageBytes = 1800 })
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stmt, err := s.PrepareContext(ctx, "SELECT a,b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	rows, err := stmt.QueryContext(ctx)
	if rows != nil {
		_ = rows.Close()
	}
	if !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("child metadata bypassed shared budget", err)
	}
}

func TestCursorColumnTransaction(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServePrepareStatus(ctx, t, p, "SELECT cursor_column FROM t", 91, 0, nil, 5) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 128)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("n", 8, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 133, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	stmt, err := tr.PrepareContext(ctx, "SELECT cursor_column FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var child sql.Rows
	var n int64
	if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&n) != nil || n != 42 {
		t.Fatal("transaction child unreadable", n, rows.Err(), child.Err())
	}
}
