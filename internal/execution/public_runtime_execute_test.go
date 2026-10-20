package execution_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	errors "errors"
	math "math"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	command "github.com/cocosip/go-oceanbase/internal/command"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	rowset "github.com/cocosip/go-oceanbase/internal/rowset"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valuecodec "github.com/cocosip/go-oceanbase/internal/valuecodec"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTextColumnFlags(t *testing.T) {
	c, err := wire.DecodeColumn(testpeer.TextColumn("v", 8, 63, 33))
	if err != nil || c.Flags != 33 {
		t.Fatal(c, err)
	}
	r := rowset.New(context.Background(), config.ModeMySQL, false, []rowset.Set{{Columns: []wire.Column{c}}})
	if nullable, known := r.ColumnTypeNullable(0); nullable || !known {
		t.Fatal(nullable, known)
	}
}

func TestTextInvalidResponses(t *testing.T) {
	const unknownType = "unknown-type"
	for _, kind := range []string{unknownType, "truncated-row"} {
		t.Run(kind, func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "SELECT v") {
					return
				}
				if kind == unknownType {
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 200, 63, 0)) {
						return
					}
				} else if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{3, '1'}) {
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid stream retained")
				}
			})
			defer func() { _ = db.Close() }()
			rows, err := db.QueryContext(context.Background(), "SELECT v")
			if kind == unknownType {
				if !errors.Is(err, oceanbase.ErrUnsupported) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if rows.Next() || !errors.Is(rows.Err(), wire.ErrMalformed) {
					t.Fatal(rows.Err())
				}
			}
			if db.Stats().Idle != 0 {
				t.Fatal("invalid connection returned to pool")
			}
		})
	}
}

func TestTextMidRowServerError(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	var server *oceanbase.Error
	if rows.Next() || !errors.As(rows.Err(), &server) || server.Code != 1064 {
		t.Fatal(rows.Err())
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("complete row ERR poisoned stream", err)
	}
}

func TestTextOracleSpecialFloats(t *testing.T) {
	for _, typ := range []byte{4, 5} {
		for _, s := range []string{"NAN", "INF", "-INF"} {
			v, err := valuecodec.DecodeTextValue(wire.Column{Type: typ}, oceanbase.ModeOracle, []byte(s))
			if err != nil {
				t.Errorf("type=%d value=%s: %v", typ, s, err)
				continue
			}
			n, ok := v.(float64)
			if !ok || (s == "NAN" && !math.IsNaN(n)) || (s == "INF" && !math.IsInf(n, 1)) || (s == "-INF" && !math.IsInf(n, -1)) {
				t.Errorf("%s: %#v", s, v)
			}
		}
	}
}

func TestTextNextResultUsesQueryDeadline(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "CALL slow_second()") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("a", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{254, 0, 0, 10, 0}) {
			return
		}
		// The next statement may take longer than Close's cleanup budget.
		timer := time.NewTimer(1200 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		if err := p.WriteMessage(ctx, []byte{1}); err != nil {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("b", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '2'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "CALL slow_second()")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("unexpected first row")
	}
	if !rows.NextResultSet() {
		t.Fatalf("normal result switch used cleanup timeout: %v", rows.Err())
	}
	var n int
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestTextMaterializationLimits(t *testing.T) {
	const metadata = "metadata"
	for _, kind := range []string{metadata, "lob"} {
		t.Run(kind, func(t *testing.T) {
			cfg := oceanbase.Config{User: testUser, Limits: oceanbase.Limits{MaxMessageBytes: 128, MaxLOBBytes: 1}}
			cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				if !testpeer.ReadQuery(context.Background(), t, p, "SELECT v") {
					return
				}
				if kind == metadata {
					name := string(bytes.Repeat([]byte{'a'}, 60))
					if !testpeer.SendPackets(context.Background(), t, p, []byte{2}, testpeer.TextColumn(name, 253, 45, 0), testpeer.TextColumn(name, 253, 45, 0)) {
						return
					}
				} else {
					if !testpeer.SendPackets(context.Background(), t, p, []byte{1}, testpeer.TextColumn("v", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, 'a', 'b'}) {
						return
					}
				}
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("oversized materialization retained stream")
				}
			})
			c, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(c)
			defer func() { _ = db.Close() }()
			rows, err := db.QueryContext(context.Background(), "SELECT v")
			if kind == metadata {
				if !errors.Is(err, wire.ErrMessageTooLarge) {
					if rows != nil {
						_ = rows.Close()
					}
					t.Fatalf("aggregate metadata limit: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() || !errors.Is(rows.Err(), wire.ErrMessageTooLarge) {
				t.Fatalf("LOB materialization limit: %v", rows.Err())
			}
		})
	}
}

func TestTextJSONType(t *testing.T) {
	value, err := valuecodec.DecodeTextValue(wire.Column{Type: 245, Charset: 63}, oceanbase.ModeMySQL, []byte(`{"n":9007199254740993}`))
	if err != nil || value != `{"n":9007199254740993}` {
		t.Fatalf("JSON changed type or precision: %#v %v", value, err)
	}
}

func TestConnectQueryInitialization(t *testing.T) {
	c, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Session: oceanbase.SessionOptions{InitSQL: []string{"SELECT 1"}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, "SELECT 1") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
			return
		}
		if !testpeer.ServeOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer func() { _ = db.Close() }()
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("query initialization was not drained", err)
	}
}

func TestTextDeprecatedEOF(t *testing.T) {
	c, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.AuthGreeting(authentication.Native, false)
		// Upper capability word includes CLIENT_DEPRECATE_EOF.
		greeting[37] |= 1
		if err := p.WriteMessage(ctx, greeting); err != nil {
			t.Error(err)
			return
		}
		response, err := p.ReadMessage(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		if len(response) < 4 || response[3]&1 == 0 {
			t.Error("EOF replacement not negotiated")
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeBaseline(ctx, t, p, 2, true) {
			return
		}
		if !testpeer.ReadQuery(context.Background(), t, p, "SELECT 1") {
			return
		}
		// No metadata EOF in the negotiated protocol.
		if !testpeer.SendPackets(context.Background(), t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{1, '1'}, []byte{254, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer func() { _ = db.Close() }()
	var n int
	if err = db.QueryRowContext(context.Background(), "SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestTextServerError(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "bad SQL") {
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
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, err := db.ExecContext(context.Background(), "bad SQL")
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1064 || server.SQLState != "42000" {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("complete ERR poisoned connection", err)
	}
}

func TestTextMultiResults(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "CALL p()") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 10, 0, 0, 0}, []byte{1}, testpeer.TextColumn("a", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, testpeer.TextColumn("b", 253, 45, 0), []byte{254, 0, 0, 10, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 10, 0}, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.QueryContext(context.Background(), "CALL p()")
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
	if rows.Next() {
		t.Fatal("extra first row")
	}
	if !rows.NextResultSet() {
		t.Fatal("command result swallowed next rowset", rows.Err())
	}
	var s string
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&s); err != nil || s != "x" {
		t.Fatal(s, err)
	}
	if rows.Next() {
		t.Fatal("extra second row")
	}
	if rows.NextResultSet() || rows.Err() != nil {
		t.Fatal("tail OK became fake rowset", rows.Err())
	}
}

func TestRowsDrain(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{1, '2'}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, testpeer.TextColumn("w", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.QueryContext(context.Background(), "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatalf("early Close left unread response: %v", err)
	}
}

func TestResultCounts(t *testing.T) {
	for _, tc := range []struct {
		name               string
		response           []byte
		affected, id       int64
		affectedErr, idErr error
	}{
		{"zero", []byte{0, 0, 0, 2, 0, 0, 0}, 0, 0, nil, oceanbase.ErrUnsupported},
		{"counts", []byte{0, 5, 7, 2, 0, 0, 0}, 5, 7, nil, nil},
		{"no-insert-id", []byte{0, 5, 0, 2, 0, 0, 0}, 5, 0, nil, oceanbase.ErrUnsupported},
		{"maximum-insert-id", []byte{0, 1, 254, 255, 255, 255, 255, 255, 255, 255, 127, 2, 0, 0, 0}, 1, math.MaxInt64, nil, nil},
		{"insert-id-overflow", []byte{0, 1, 254, 255, 255, 255, 255, 255, 255, 255, 255, 2, 0, 0, 0}, 1, 0, nil, command.ErrCountOverflow},
		{"count-overflow", []byte{0, 254, 255, 255, 255, 255, 255, 255, 255, 255, 7, 2, 0, 0, 0}, 0, 7, command.ErrCountOverflow, nil},
		{"both-overflow", []byte{0, 254, 255, 255, 255, 255, 255, 255, 255, 255, 254, 255, 255, 255, 255, 255, 255, 255, 255, 2, 0, 0, 0}, 0, 0, command.ErrCountOverflow, command.ErrCountOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "UPDATE t SET v=1") {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, tc.response) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			result, err := db.ExecContext(context.Background(), "UPDATE t SET v=1")
			if err != nil {
				t.Fatal(err)
			}
			affected, err := result.RowsAffected()
			if !errors.Is(err, tc.affectedErr) || affected != tc.affected {
				t.Fatal(affected, err)
			}
			id, err := result.LastInsertId()
			if !errors.Is(err, tc.idErr) || id != tc.id {
				t.Fatal(id, err)
			}
		})
	}
}

func TestTextRows(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v,b") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("v", 253, 45, 0), testpeer.TextColumn("b", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 2, 0, 255}, []byte{251, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.QueryContext(context.Background(), "SELECT v,b")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	if err != nil || len(columns) != 2 || columns[0] != "v" || columns[1] != "b" {
		t.Fatal(columns, err)
	}
	var text sql.NullString
	var b []byte
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&text, &b); err != nil || !text.Valid || text.String != "" || !bytes.Equal(b, []byte{0, 255}) {
		t.Fatalf("first row: %v %x %v", text, b, err)
	}
	saved := b
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err = rows.Scan(&text, &b); err != nil || text.Valid || b == nil || len(b) != 0 {
		t.Fatalf("NULL and empty confused: %v %x %v", text, b, err)
	}
	if !bytes.Equal(saved, []byte{0, 255}) {
		t.Fatal("previous row buffer overwritten")
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}
