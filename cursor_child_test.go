package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testCursorCycle  = "cycle"
	testCursorNested = "nested"
	testCursorDepth  = "depth"
)

func TestCursorSensitiveInitialChildren(t *testing.T) {
	const (
		query      = "SELECT child FROM cursor_fixture"
		nullChild  = "null-child"
		parent     = "parent"
		fetchError = "fetch-error"
	)
	for _, kind := range []string{"child", nullChild, parent, fetchError} {
		t.Run(kind, func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, query, 54, 0, nil) {
					return
				}
				payload, err := wire.EncodePrepareExecute(54, []byte(query), nil, 4096)
				if err != nil {
					t.Error(err)
					return
				}
				payload[5] = 1
				binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
				row := binary.LittleEndian.AppendUint32([]byte{0, 0}, 92)
				switch kind {
				case nullChild:
					row = []byte{0, 4}
				case parent:
					row = binary.LittleEndian.AppendUint32([]byte{0, 0}, 54)
				}
				if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, combinedHeader(54, 1, 0, 0), textColumn("child", 163, 63, 0), []byte{254, 0, 0, 70, 0}, row, []byte{254, 0, 0, 70, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
					return
				}
				if kind == parent {
					p.ResetSequence()
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("parent reference retained the connection or sent another command")
					}
					return
				}
				if kind != nullChild {
					if !readCommand(ctx, t, p, cursorFetchFixture(92, 128)) {
						return
					}
					if kind == fetchError {
						if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', 'H', 'Y', '0', '0', '0', 'x'}) {
							return
						}
						p.ResetSequence()
						if _, err := p.ReadMessage(ctx); err == nil {
							t.Error("failed child fetch retained the connection")
						}
						return
					}
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 92)) {
						return
					}
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 54)) || !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			session, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			cur, err := OpenCursor(ctx, session, query, CursorOptions{Mode: CursorSensitive})
			switch kind {
			case parent:
				if cur != nil || !errors.Is(err, ErrCursor) {
					t.Fatal("parent reference accepted", cur, err)
				}
				return
			case fetchError:
				var serverError *Error
				if cur != nil || !errors.As(err, &serverError) || serverError.Code != 1064 {
					t.Fatal("child failure lost", cur, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = session.PingContext(ctx); err != nil {
				t.Fatal("initial child cleanup damaged parent session", err)
			}
		})
	}
}

func TestCursorColumnTransaction(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !servePrepareStatus(ctx, t, p, "SELECT cursor_column FROM t", 91, 0, nil, 5) || !readCommand(ctx, t, p, executeHeader(91)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}) {
			return
		}
		if !readCommand(ctx, t, p, cursorFetchFixture(92, 128)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("n", 8, 63, 0), []byte{254, 0, 0, 5, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 133, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestCursorStaticChildren(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewind", true: "cross-batch-budget"}[budget], func(t *testing.T) {
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(91)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 70, 0}) {
					return
				}
				batches := 1
				if budget {
					batches = 2
				}
				for i := 0; i < batches; i++ {
					last := byte(134)
					if budget && i == 0 {
						last = 70
					}
					fetch := append(cursorFetchFixture(91, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
					if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, uint32(92+i)), []byte{254, 0, 0, last, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) {
						return
					}
					if !readCommand(ctx, t, p, cursorFetchFixture(uint32(92+i), 1)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !readCommand(ctx, t, p, stmtPacket(25, uint32(92+i))) {
						return
					}
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 91)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err = s.Raw(func(dc any) error { dc.(*conn).cfg.Limits.FetchSize = 1; return nil }); err != nil {
				t.Fatal(err)
			}
			opts := CursorOptions{Mode: CursorStatic}
			if budget {
				opts.MaxCacheBytes = 600
			}
			openCtx, cancelOpen := context.WithCancel(ctx)
			defer cancelOpen()
			cur, err := OpenCursor(openCtx, s, "SELECT child FROM t", opts)
			if budget {
				if cur != nil {
					_ = cur.Close()
				}
				if !errors.Is(err, wire.ErrMessageTooLarge) {
					t.Fatal("nested snapshots bypassed total cache limit", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cur.Close() }()
			for i := 0; i < 2; i++ {
				rows, err := cur.Fetch(ctx, CursorPosition{Origin: CursorAbsolute, Offset: 1}, 1)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				var child sql.Rows
				var n int64
				if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&n) != nil || n != 42 {
					t.Fatal("static child was reused or inherited Open context", i, n, child.Err())
				}
				if err = rows.Close(); err != nil {
					t.Fatal(err)
				}
				cancelOpen()
			}
		})
	}
}

func TestCursorColumnSharedMetadataBudget(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		row := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), 93)
		if !servePrepare(ctx, t, p, "SELECT a,b FROM t", 91, 0, nil) || !readCommand(ctx, t, p, executeHeader(91)) || !sendPackets(ctx, t, p, []byte{2}, textColumn("a", 163, 63, 0), textColumn("b", 163, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) {
			return
		}
		col := textColumn("n", 8, 63, 0)
		col = append(complexLenBytes(nil, []byte(strings.Repeat("c", 900))), col[4:]...)
		for _, id := range []uint32{92, 93} {
			if !readCommand(ctx, t, p, cursorFetchFixture(id, 128)) || !sendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !readCommand(ctx, t, p, stmtPacket(25, id)) {
				return
			}
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Raw(func(dc any) error { dc.(*conn).cfg.Limits.MaxMessageBytes = 1800; return nil }); err != nil {
		t.Fatal(err)
	}
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
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !readCommand(ctx, t, p, executeHeader(91)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) {
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
					if !readCommand(ctx, t, p, cursorFetchFixture(id, 128)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", typ, 63, 0), []byte{254, 0, 0, 6, 0}, value, []byte{254, 0, 0, 134, 0}) {
						return
					}
				}
				for i := count - 1; i >= 0; i-- {
					if !readCommand(ctx, t, p, stmtPacket(25, uint32(92+i))) {
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
				if !readCommand(ctx, t, p, stmtPacket(25, 91)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
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
				if !errors.Is(err, ErrCursor) {
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

func TestCursorColumnFetchCancellation(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !readCommand(ctx, t, p, executeHeader(91)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(92, 128)) {
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

func TestCursorFetchChildBudget(t *testing.T) {
	col := textColumn("child", 163, 63, 0)
	col = append(complexLenBytes(nil, []byte(strings.Repeat("c", 900))), col[4:]...)
	childCol := append(complexLenBytes(nil, []byte(strings.Repeat("c", 900))), textColumn("n", 8, 63, 0)[4:]...)
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT child FROM t", 91, 0, nil) || !readCommand(ctx, t, p, cursorExecuteFixture(91)) || !sendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 70, 0}) {
			return
		}
		fetch := append(cursorFetchFixture(91, 1), 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		if !readCommand(ctx, t, p, fetch) || !sendPackets(ctx, t, p, []byte{1}, col, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 134, 0}, []byte{0, 1, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, cursorFetchFixture(92, 128)) || !sendPackets(ctx, t, p, []byte{1}, childCol, []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 92)) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Raw(func(dc any) error { dc.(*conn).cfg.Limits.MaxMessageBytes = 1800; return nil }); err != nil {
		t.Fatal(err)
	}
	cur, err := OpenCursor(ctx, s, "SELECT child FROM t", CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cur.Close() }()
	rows, err := cur.Fetch(ctx, CursorPosition{}, 1)
	if rows != nil {
		_ = rows.Close()
	}
	if !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("parent metadata bypassed shared fetch budget", err)
	}
}
