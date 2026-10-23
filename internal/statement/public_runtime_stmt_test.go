package statement_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	math "math"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestStmtCloseBounded(t *testing.T) {
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Limits: oceanbase.Limits{CleanupTimeout: 20 * time.Millisecond}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !publictest.ServePrepare(ctx, t, p, "SELECT held_rows", 37, 0, nil) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(37)) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}) {
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

func TestStmtPhysicalOwnership(t *testing.T) {
	var sessions atomic.Uint32
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		id := sessions.Add(1)
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, id, 1, nil) {
			return
		}
		if id == 2 {
			payload := append(publictest.ExecuteHeader(id), 0, 1, 8, 0)
			payload = binary.LittleEndian.AppendUint64(payload, 9)
			if !testpeer.ServeOK(ctx, t, p, payload, 2) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, id)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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

func TestServerPrepareMultiResults(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "CALL p(?)", 33, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(33), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, testpeer.TextColumn("s", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 'x'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 33)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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

func TestServerPrepareError(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, "unsupported SQL"...)) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.PrepareContext(context.Background(), "unsupported SQL"); !errors.As(err, new(*oceanbase.Error)) {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerPrepareDeprecatedEOF(t *testing.T) {
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.AuthGreeting(authentication.Native, false)
		greeting[37] |= 1
		greeting[36] |= 4
		if !testpeer.SendPackets(ctx, t, p, greeting) {
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil || len(response) < 4 || response[2]&4 == 0 || response[3]&1 == 0 {
			t.Error("prepare capabilities not negotiated", err)
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeBaseline(ctx, t, p, 2, true) {
			return
		}
		ctx = context.Background()
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, selectBindSQL...)) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 31, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}, testpeer.TextColumn("p", 253, 45, 0), testpeer.TextColumn("v", 8, 63, 0)) {
			return
		}
		payload := append(publictest.ExecuteHeader(31), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 8)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{0, 0, 8, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0, 0, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 31)) {
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
	var n int
	if err = db.QueryRowContext(context.Background(), selectBindSQL, 8).Scan(&n); err != nil || n != 8 {
		t.Fatal(n, err)
	}
}

func TestServerPrepareOracleInfinity(t *testing.T) {
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: oceanbase.ModeOracle, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !publictest.ServePrepare(ctx, t, p, "SELECT ? FROM dual", 29, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(29), 0, 1, 1, 1)
		payload = binary.LittleEndian.AppendUint64(payload, math.Float64bits(math.Inf(1)))
		if !testpeer.ServeOK(ctx, t, p, payload, 6) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 29)) {
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
	if _, err = db.ExecContext(context.Background(), "SELECT :x FROM dual", sql.Named("x", math.Inf(1))); err != nil {
		t.Fatal(err)
	}
}

func TestServerPrepareRowsCleanupFailure(t *testing.T) {
	dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 27, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(27), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("failed close completed")
		}
	})
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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

func TestServerPrepareOracleNamed(t *testing.T) {
	query := "SELECT :x,:X FROM dual"
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: oceanbase.ModeOracle, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !publictest.ServePrepare(ctx, t, p, "SELECT ?,? FROM dual", 23, 2, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(23), 0, 1, 253, 0, 253, 0, 4, '1', '.', '2', '0', 4, '1', '.', '2', '0')
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("a", 246, 45, 0), testpeer.TextColumn("b", 3, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 4, '1', '.', '2', '0', 42, 0, 0, 0}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 23)) {
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

func TestServerPrepareCleanupFailure(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 21, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(21), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		_ = testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0})
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), selectBindSQL, 1); err == nil {
		t.Fatal("failed statement cleanup was hidden")
	}
}

func TestServerPrepareNoReplay(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 13, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(13), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		// Peer closes after accepting execution; the result is unknown.
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), selectBindSQL, 1); err == nil || errors.Is(err, driver.ErrBadConn) || !errors.As(err, new(*wire.IOError)) {
		t.Fatalf("execution lost response: %v", err)
	}
}

func TestStmtOwnership(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, selectBindSQL, 11, 1, nil) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 11)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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

func TestServerPrepareImplicit(t *testing.T) {
	query := `SELECT ? AS "a\\"`
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 9, 1, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(9), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 5)
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 0, 5, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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

func TestServerPrepare(t *testing.T) {
	query := "INSERT INTO t VALUES (?,?)"
	first := append(publictest.ExecuteHeader(7), 0, 1, 8, 0, 8, 128)
	first = binary.LittleEndian.AppendUint64(first, 42)
	first = binary.LittleEndian.AppendUint64(first, math.MaxUint64)
	second := append(publictest.ExecuteHeader(7), 2, 1, 253, 0, 6, 0, 1, 'x')
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 7, 2, nil) {
			return
		}
		for _, payload := range [][]byte{first, second} {
			if !testpeer.ServeOK(ctx, t, p, payload, 2) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
