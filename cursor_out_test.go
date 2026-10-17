package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func serveResourceOut(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, columns [][]byte, row []byte, ordinary, tailError bool) bool {
	t.Helper()
	params := make([]wire.Parameter, len(columns))
	for i := range params {
		params[i].Type = 6
	}
	want, err := wire.EncodePrepareExecute(0, []byte(query), params, 1<<20)
	if err != nil || !readCommand(ctx, t, p, want) {
		return false
	}
	flags, n := byte(4), uint16(len(columns))
	if ordinary {
		flags, n = 0, 1
	}
	if !sendPackets(ctx, t, p, combinedHeader(91, n, uint16(len(columns)), flags)) {
		return false
	}
	for range columns {
		if !sendPackets(ctx, t, p, textColumn("p", 6, 63, 0)) {
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	if ordinary {
		if !sendPackets(ctx, t, p, textColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{byte(len(columns))}) {
			return false
		}
	}
	for _, col := range columns {
		if !sendPackets(ctx, t, p, col) {
			return false
		}
	}
	endStatus := byte(6)
	if ordinary {
		endStatus = 14
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 16}, row, []byte{254, 0, 0, endStatus, 0}) {
		return false
	}
	tail := []byte{0, 0, 0, 6, 0, 0, 0}
	if tailError {
		tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
	}
	return sendPackets(ctx, t, p, tail)
}

func TestCursorOutPublish(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{true: "early-close", false: "consume"}[early], func(t *testing.T) {
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !serveResourceOut(ctx, t, p, "CALL p(?)", [][]byte{textColumn("cursor", 163, 63, 0)}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), true, false) {
					return
				}
				if !readCommand(ctx, t, p, cursorFetchFixture(92, 2)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("value", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 134, 0}) {
					return
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) || !serveOK(ctx, t, p, []byte{14}, 6) {
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
			old, cancelOld := context.WithCancel(ctx)
			var cur *Cursor
			e, err := Execute(old, s, "CALL p(?)", CursorOut{Dest: &cur})
			if err != nil || cur != nil {
				t.Fatal(err, cur)
			}
			if !early {
				for e.Rows().Next() {
				}
			}
			if err = e.Close(); err != nil || cur == nil {
				t.Fatal(err, cur)
			}
			cancelOld()
			rows, err := cur.Fetch(ctx, CursorPosition{}, 2)
			if err != nil {
				t.Fatal(err)
			}
			var n int64
			if !rows.Next() || rows.Scan(&n) != nil || n != 42 {
				t.Fatal(n, rows.Err())
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOutputLease(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple", true: "scanner-failure"}[fail], func(t *testing.T) {
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				row := binary.LittleEndian.AppendUint32([]byte{0, 0}, 92)
				row = binary.LittleEndian.AppendUint32(row, 93)
				row = binary.LittleEndian.AppendUint64(row, 7)
				if !serveResourceOut(ctx, t, p, "CALL p(?,?,?)", [][]byte{textColumn("a", 163, 63, 0), textColumn("b", 163, 63, 0), textColumn("c", 8, 63, 0)}, row, false, false) {
					return
				}
				ids := map[uint32]bool{}
				for range 3 {
					p.ResetSequence()
					b, err := p.ReadMessage(ctx)
					if err != nil || len(b) != 5 || b[0] != 25 {
						t.Error("expected cursor/parent close", b, err)
						return
					}
					id := binary.LittleEndian.Uint32(b[1:])
					if ids[id] || id < 91 || id > 93 || len(ids) < 2 && id == 91 {
						t.Error("lease close order", ids, id)
						return
					}
					ids[id] = true
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
			var a, b *Cursor
			var n int64
			var dest any = &n
			scanner := &failingOutScanner{}
			if fail {
				dest = scanner
			}
			e, err := Execute(ctx, s, "CALL p(?,?,?)", CursorOut{Dest: &a}, CursorOut{Dest: &b}, Out{Dest: dest, Type: "BIGINT"})
			if fail {
				if !errors.Is(err, ErrOut) || a != nil || b != nil || scanner.calls != 1 {
					t.Fatal(err, a, b, scanner.calls)
				}
				return
			}
			if err != nil || e.Close() != nil || a == nil || b == nil || n != 7 {
				t.Fatal(err, a, b, n)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCursorColumn(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT cursor_column FROM t", 91, 0, nil) || !readCommand(ctx, t, p, executeHeader(91)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !readCommand(ctx, t, p, cursorFetchFixture(92, 128)) || !sendPackets(ctx, t, p, []byte{1}, textColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), binary.LittleEndian.AppendUint64([]byte{0, 0}, 43), []byte{254, 0, 0, 134, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) {
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
	stmt, err := s.PrepareContext(ctx, "SELECT cursor_column FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var child sql.Rows
	if !rows.Next() || rows.Scan(&child) != nil {
		t.Fatal(rows.Err())
	}
	var n int64
	if !child.Next() || child.Scan(&n) != nil || n != 42 {
		t.Fatal(n, child.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if child.Next() {
		t.Fatal("child survived parent Rows.Close")
	}
}

func TestOutputLOBHandles(t *testing.T) {
	for _, null := range []bool{false, true} {
		t.Run(map[bool]string{false: "streaming", true: "sql-null"}[null], func(t *testing.T) {
			locator := lobOutFixture(1 << 32)
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				row := []byte{0, 28}
				if !null {
					row = binary.LittleEndian.AppendUint32([]byte{0, 0}, 92)
					row = append(append(row, byte(len(locator))), locator...)
					clob := lobInlineFixture("你好")
					row = append(append(row, byte(len(clob))), clob...)
				}
				if !serveResourceOut(ctx, t, p, "CALL p(?,?,?)", [][]byte{textColumn("a", 163, 63, 0), textColumn("b", 210, 63, 0), textColumn("c", 211, 45, 0)}, row, false, false) {
					return
				}
				if !null {
					params := []wire.Parameter{lobIntFixture(3), {Type: 210, Data: locator, LengthEncoded: true}, lobIntFixture(1), {Type: 6}, {Type: 6}}
					result := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 3), 3, 'a', 'b', 'c')
					if !serveLOBCall(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, result) || !readCommand(ctx, t, p, stmtPacket(25, 92)) || !serveOK(ctx, t, p, []byte{14}, 6) {
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
			var cur *Cursor
			var blob *Blob
			var clob *Clob
			e, err := Execute(ctx, s, "CALL p(?,?,?)", CursorOut{Dest: &cur}, Out{Dest: &blob, Type: typeNameBlob}, Out{Dest: &clob, Type: typeNameClob})
			if err != nil || e.Close() != nil {
				t.Fatal(err)
			}
			if null {
				if cur != nil || blob != nil || clob != nil {
					t.Fatal("NULL produced handles")
				}
				return
			}
			if cur == nil || blob == nil || clob == nil {
				t.Fatal("missing mixed handles")
			}
			if size, err := blob.Size(ctx); err != nil || size != 1<<32 {
				t.Fatal(size, err)
			}
			data := make([]byte, 3)
			if n, err := blob.ReadAtContext(ctx, data, 0); err != nil || n != 3 || string(data) != "abc" {
				t.Fatal(n, string(data), err)
			}
			if text, err := clob.ReadChars(ctx, 0, 2); err != nil || text != "你好" {
				t.Fatal(text, err)
			}
			if err = blob.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cur.Close(); err != nil {
				t.Fatal(err)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal("parent closed with a clob lease", err)
			}
			if err = clob.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorOutPreSend(t *testing.T) {
	var cur *Cursor
	var blob *Blob
	var clob *Clob
	occupied := &Cursor{}
	for _, arg := range []any{CursorOut{}, CursorOut{Dest: &occupied}, Out{Dest: &blob}, sql.Out{Dest: &blob}, Out{Dest: &blob, Type: typeNameClob}, Out{Dest: &clob, Type: typeNameClob, In: true}, Out{Dest: &blob, Type: typeNameBlob, Length: 1}} {
		if _, err := convertArgument(arg); !errors.Is(err, ErrOut) {
			t.Fatalf("%T: %v", arg, err)
		}
	}
	for _, arg := range []any{CursorOut{Dest: &cur}, Out{Dest: &blob, Type: typeNameBlob}, Out{Dest: &clob, Type: typeNameNClob}} {
		for _, query := range []bool{false, true} {
			c := outputTestConn(t, 0, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); serveQuit(t, p) })
			var err error
			args := []driver.NamedValue{{Ordinal: 1, Value: arg}}
			if query {
				_, err = c.QueryContext(context.Background(), "CALL p(?)", args)
			} else {
				_, err = c.ExecContext(context.Background(), "CALL p(?)", args)
			}
			if !errors.Is(err, ErrOut) {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type callbackOutScanner struct{ scan func() }

func (s callbackOutScanner) Scan(any) error { s.scan(); return nil }

func TestCursorOutCancelDuringConversion(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		row := binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), 7)
		if !serveResourceOut(ctx, t, p, "CALL p(?,?)", [][]byte{textColumn("a", 163, 63, 0), textColumn("b", 8, 63, 0)}, row, false, false) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) {
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
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	var cur *Cursor
	e, err := Execute(callCtx, s, "CALL p(?,?)", CursorOut{Dest: &cur}, Out{Dest: &callbackOutScanner{scan: cancelCall}, Type: "BIGINT"})
	if e != nil {
		_ = e.Close()
	}
	if !errors.Is(err, context.Canceled) || cur != nil {
		t.Fatal("published canceled outputs", err, cur)
	}
}

func TestCursorOutTailFailure(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(map[bool]string{false: "outputs-only", true: "ordinary-first"}[ordinary], func(t *testing.T) {
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				if !serveResourceOut(ctx, t, p, "CALL p(?)", [][]byte{textColumn("a", 163, 63, 0)}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), ordinary, true) {
					return
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) || !serveOK(ctx, t, p, []byte{14}, 6) {
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
			var cur *Cursor
			e, err := Execute(ctx, s, "CALL p(?)", CursorOut{Dest: &cur})
			if ordinary && err != nil {
				t.Fatal(err)
			}
			if e != nil {
				err = e.Close()
			}
			var server *Error
			if !errors.As(err, &server) || cur != nil {
				t.Fatal("tail error published handle", err, cur)
			}
			if err = s.PingContext(ctx); err != nil {
				t.Fatal("complete server error discarded session", err)
			}
		})
	}
}

func TestCursorOutMalformed(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, _ := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(91, 1, 1, 4), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("a", 163, 63, 0), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 92, 0}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("malformed output retained physical connection")
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
	var cur *Cursor
	_, err = Execute(ctx, s, "CALL p(?)", CursorOut{Dest: &cur})
	if !errors.Is(err, wire.ErrMalformed) || cur != nil {
		t.Fatal(err, cur)
	}
}

func TestCursorOutCapability(t *testing.T) {
	for _, tc := range []struct {
		mode    Mode
		version string
	}{{ModeMySQL, testOutVersion}, {ModeOracle, "2.2.75"}, {ModeOracle, ""}} {
		c := outputTestConn(t, 0, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); serveQuit(t, p) })
		c.session.mode, c.session.obVersion = tc.mode, tc.version
		var cur *Cursor
		ctx := context.WithValue(context.Background(), executionContextKey{}, &Execution{session: new(sql.Conn)})
		if _, err := c.queryOutputs(ctx, "CALL p(?)", []driver.NamedValue{{Ordinal: 1, Value: CursorOut{Dest: &cur}}}, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutputLeaseTransaction(t *testing.T) {
	db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) {
			return
		}
		want, _ := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(91, 1, 1, 4), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}, textColumn("a", 163, 63, 0), []byte{254, 0, 0, 5, 16}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 5, 0}, []byte{0, 0, 0, 5, 0, 0, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 92)) || !readCommand(ctx, t, p, stmtPacket(25, 91)) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
	var cur *Cursor
	e, err := Execute(ctx, tr, "CALL p(?)", CursorOut{Dest: &cur})
	if err != nil || e.Close() != nil || cur == nil {
		t.Fatal(err, cur)
	}
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = cur.Fetch(ctx, CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, ErrResource) {
		t.Fatal("output cursor survived transaction", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}
