package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"github.com/cocosip/go-oceanbase/internal/testutil"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func readCommand(ctx context.Context, t *testing.T, p *wire.PacketIO, want []byte) bool {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil || !bytes.Equal(b, want) {
		t.Errorf("command got %x want %x: %v", b, want, err)
		return false
	}
	return true
}
func servePrepare(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte) bool {
	t.Helper()
	return servePrepareStatus(ctx, t, p, query, id, params, columns, 2)
}
func servePrepareStatus(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id uint32, params int, columns [][]byte, status byte) bool {
	t.Helper()
	if !readCommand(ctx, t, p, append([]byte{22}, query...)) {
		return false
	}
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(columns)))
	b = binary.LittleEndian.AppendUint16(b, uint16(params))
	b = append(b, 0, 0, 0)
	if !sendPackets(ctx, t, p, b) {
		return false
	}
	for i := 0; i < params; i++ {
		if !sendPackets(ctx, t, p, textColumn("p", 253, 45, 0)) {
			return false
		}
	}
	if params > 0 && !sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, c := range columns {
		if !sendPackets(ctx, t, p, c) {
			return false
		}
	}
	return len(columns) == 0 || sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}
func stmtPacket(cmd byte, id uint32) []byte { return binary.LittleEndian.AppendUint32([]byte{cmd}, id) }
func executeHeader(id uint32) []byte        { return append(stmtPacket(23, id), 0, 1, 0, 0, 0) }

func TestServerPrepare(t *testing.T) {
	query := "INSERT INTO t VALUES (?,?)"
	first := append(executeHeader(7), 0, 1, 8, 0, 8, 128)
	first = binary.LittleEndian.AppendUint64(first, 42)
	first = binary.LittleEndian.AppendUint64(first, math.MaxUint64)
	second := append(executeHeader(7), 2, 1, 253, 0, 6, 0, 1, 'x')
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 7, 2, nil) {
			return
		}
		for _, payload := range [][]byte{first, second} {
			if !serveOK(ctx, t, p, payload, 2) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	prepareCtx, cancelPrepare := context.WithCancel(context.Background())
	s, err := db.PrepareContext(prepareCtx, query)
	cancelPrepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]any{{int64(42), uint64(math.MaxUint64)}, {"x", nil}} {
		r, err := s.ExecContext(context.Background(), args...)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := r.RowsAffected(); err != nil || n != 0 {
			t.Fatal(n, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(context.Background(), 1, 2); err == nil {
		t.Fatal("closed statement executed")
	}
}

func TestServerPrepareImplicit(t *testing.T) {
	query := `SELECT ? AS "a\\"`
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 9, 1, nil) {
			return
		}
		payload := append(executeHeader(9), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 5)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 5, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 9)) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), query, 5).Scan(&n); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStmtOwnership(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, selectBindSQL, 11, 1, nil) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 11)) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), selectBindSQL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(context.Background()); err == nil {
		t.Fatal("missing argument executed")
	}
	if _, err = s.ExecContext(context.Background(), 1, 2); err == nil {
		t.Fatal("extra argument executed")
	}
	if _, err = s.ExecContext(context.Background(), sql.Named("x", 1)); err == nil {
		t.Fatal("MySQL named argument executed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerPrepareNoReplay(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, selectBindSQL, 13, 1, nil) {
			return
		}
		payload := append(executeHeader(13), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		// Peer closes after accepting execution; the result is unknown.
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), selectBindSQL, 1); err == nil || errors.Is(err, driver.ErrBadConn) || !errors.As(err, new(*wire.IOError)) {
		t.Fatalf("execution lost response: %v", err)
	}
}

func TestServerPrepareCleanupFailure(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, selectBindSQL, 21, 1, nil) {
			return
		}
		payload := append(executeHeader(21), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		_ = sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0})
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), selectBindSQL, 1); err == nil {
		t.Fatal("failed statement cleanup was hidden")
	}
}

func TestServerPrepareOracleNamed(t *testing.T) {
	query := "SELECT :x,:X FROM dual"
	connector, err := NewConnector(Config{User: testUser, Mode: ModeOracle, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) || !serveBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !servePrepare(ctx, t, p, "SELECT ?,? FROM dual", 23, 2, nil) {
			return
		}
		payload := append(executeHeader(23), 0, 1, 253, 0, 253, 0, 4, '1', '.', '2', '0', 4, '1', '.', '2', '0')
		if !readCommand(ctx, t, p, payload) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{2}, textColumn("a", 246, 45, 0), textColumn("b", 3, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 4, '1', '.', '2', '0', 42, 0, 0, 0}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 23)) {
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
	s, err := db.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var a, b string
	if err = s.QueryRowContext(context.Background(), sql.Named("x", "1.20")).Scan(&a, &b); err != nil || a != "1.20" || b != "42" {
		t.Fatal(a, b, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

type closeFailureConn struct{ net.Conn }

func (c closeFailureConn) Write(data []byte) (int, error) {
	if len(data) == 5 && data[0] == 25 {
		return 0, testutil.ErrInjected
	}
	return c.Conn.Write(data)
}
func TestServerPrepareRowsCleanupFailure(t *testing.T) {
	dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !servePrepare(ctx, t, p, selectBindSQL, 27, 1, nil) {
			return
		}
		payload := append(executeHeader(27), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("failed close completed")
		}
	})
	connector, err := NewConnector(Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return closeFailureConn{raw}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(context.Background(), selectBindSQL, 1); !errors.Is(err, testutil.ErrInjected) {
		t.Fatalf("failed rows cleanup was hidden: %v", err)
	}
}

func TestServerPrepareOracleInfinity(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Mode: ModeOracle, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) || !serveBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !servePrepare(ctx, t, p, "SELECT ? FROM dual", 29, 1, nil) {
			return
		}
		payload := append(executeHeader(29), 0, 1, 5, 0)
		payload = binary.LittleEndian.AppendUint64(payload, math.Float64bits(math.Inf(1)))
		if !serveOK(ctx, t, p, payload, 6) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 29)) {
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
	if _, err = db.ExecContext(context.Background(), "SELECT :x FROM dual", sql.Named("x", math.Inf(1))); err != nil {
		t.Fatal(err)
	}
}

func TestServerPrepareDeprecatedEOF(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := authGreeting(authNative, false)
		greeting[37] |= 1
		greeting[36] |= 4
		if !sendPackets(ctx, t, p, greeting) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || len(response) < 4 || response[2]&4 == 0 || response[3]&1 == 0 {
			t.Error("prepare capabilities not negotiated", err)
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveBaseline(ctx, t, p, 2, true) {
			return
		}
		ctx = context.Background()
		if !readCommand(ctx, t, p, append([]byte{22}, selectBindSQL...)) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 31, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}, textColumn("p", 253, 45, 0), textColumn("v", 8, 63, 0)) {
			return
		}
		payload := append(executeHeader(31), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 8)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{0, 0, 8, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0, 0, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 31)) {
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
	var n int
	if err = db.QueryRowContext(context.Background(), selectBindSQL, 8).Scan(&n); err != nil || n != 8 {
		t.Fatal(n, err)
	}
}

func TestServerPrepareError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readCommand(ctx, t, p, append([]byte{22}, "unsupported SQL"...)) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.PrepareContext(context.Background(), "unsupported SQL"); !errors.As(err, new(*Error)) {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerPrepareMultiResults(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "CALL p(?)", 33, 1, nil) {
			return
		}
		payload := append(executeHeader(33), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !readCommand(ctx, t, p, payload) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, textColumn("s", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 'x'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 33)) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "CALL p(?)", 1)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if rows.Next() || !rows.NextResultSet() {
		t.Fatal("missing second rowset", rows.Err())
	}
	var s string
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&s); err != nil || s != "x" {
		t.Fatal(s, err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStmtPhysicalOwnership(t *testing.T) {
	var sessions atomic.Uint32
	connector, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		id := sessions.Add(1)
		if !servePrepare(ctx, t, p, selectBindSQL, id, 1, nil) {
			return
		}
		if id == 2 {
			payload := append(executeHeader(id), 0, 1, 8, 0)
			payload = binary.LittleEndian.AppendUint64(payload, 9)
			if !serveOK(ctx, t, p, payload, 2) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, id)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), selectBindSQL)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if sessions.Load() != 2 {
		t.Fatal("statement was not prepared independently", sessions.Load())
	}
}

func TestStmtCloseBounded(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Limits: Limits{CleanupTimeout: 20 * time.Millisecond}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !servePrepare(ctx, t, p, "SELECT held_rows", 37, 0, nil) {
			return
		}
		if !readCommand(ctx, t, p, executeHeader(37)) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("connection survived failed cleanup")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	s, err := raw.(driver.ConnPrepareContext).PrepareContext(context.Background(), "SELECT held_rows")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.(driver.StmtQueryContext).QueryContext(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = s.Close()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || raw.(driver.Validator).IsValid() {
		t.Fatal("unbounded statement close", err, time.Since(start))
	}
	_ = rows.Close()
}
