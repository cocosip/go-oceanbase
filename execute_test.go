package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func textColumn(name string, typ byte, charset uint16, flags uint16) []byte {
	data := []byte{3, 'd', 'e', 'f', 0, 0, 0, byte(len(name))}
	data = append(data, name...)
	return append(data, 0, 12, byte(charset), byte(charset>>8), 10, 0, 0, 0, typ, byte(flags), byte(flags>>8), 0, 0, 0)
}

func queryDB(t *testing.T, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		serve(context.Background(), p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func readQuery(ctx context.Context, t testing.TB, p *wire.PacketIO, want string) bool {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil || string(b) != "\x03"+want {
		t.Errorf("expected query %q: %q %v", want, b, err)
		return false
	}
	return true
}

func sendPackets(ctx context.Context, t testing.TB, p *wire.PacketIO, packets ...[]byte) bool {
	t.Helper()
	for _, b := range packets {
		if err := p.WriteMessage(ctx, b); err != nil {
			t.Error(err)
			return false
		}
	}
	return true
}

func TestTextRows(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v,b") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{2}, textColumn("v", 253, 45, 0), textColumn("b", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{0, 2, 0, 255}, []byte{251, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestResultCounts(t *testing.T) {
	for _, tc := range []struct {
		name               string
		response           []byte
		affected, id       int64
		affectedErr, idErr error
	}{
		{"zero", []byte{0, 0, 0, 2, 0, 0, 0}, 0, 0, nil, ErrUnsupported},
		{"counts", []byte{0, 5, 7, 2, 0, 0, 0}, 5, 7, nil, nil},
		{"no-insert-id", []byte{0, 5, 0, 2, 0, 0, 0}, 5, 0, nil, ErrUnsupported},
		{"maximum-insert-id", []byte{0, 1, 254, 255, 255, 255, 255, 255, 255, 255, 127, 2, 0, 0, 0}, 1, math.MaxInt64, nil, nil},
		{"insert-id-overflow", []byte{0, 1, 254, 255, 255, 255, 255, 255, 255, 255, 255, 2, 0, 0, 0}, 1, 0, nil, errCountOverflow},
		{"count-overflow", []byte{0, 254, 255, 255, 255, 255, 255, 255, 255, 255, 7, 2, 0, 0, 0}, 0, 7, errCountOverflow, nil},
		{"both-overflow", []byte{0, 254, 255, 255, 255, 255, 255, 255, 255, 255, 254, 255, 255, 255, 255, 255, 255, 255, 255, 2, 0, 0, 0}, 0, 0, errCountOverflow, errCountOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "UPDATE t SET v=1") {
					return
				}
				if !sendPackets(ctx, t, p, tc.response) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
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

func TestRowsDrain(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{1, '2'}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, textColumn("w", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestTextMultiResults(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "CALL p()") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 10, 0, 0, 0}, []byte{1}, textColumn("a", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{1, '1'}, []byte{254, 0, 0, 10, 0}, []byte{0, 5, 0, 10, 0, 0, 0}, []byte{1}, textColumn("b", 253, 45, 0), []byte{254, 0, 0, 10, 0}, []byte{1, 'x'}, []byte{254, 0, 0, 10, 0}, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestTextServerError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "bad SQL") {
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
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, err := db.ExecContext(context.Background(), "bad SQL")
	var server *Error
	if !errors.As(err, &server) || server.Code != 1064 || server.SQLState != "42000" {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("complete ERR poisoned connection", err)
	}
}

func TestTextDeprecatedEOF(t *testing.T) {
	c, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := authGreeting(authNative, false)
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
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveBaseline(ctx, t, p, 2, true) {
			return
		}
		if !readQuery(context.Background(), t, p, "SELECT 1") {
			return
		}
		// No metadata EOF in the negotiated protocol.
		if !sendPackets(context.Background(), t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{1, '1'}, []byte{254, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestConnectQueryInitialization(t *testing.T) {
	c, err := NewConnector(Config{User: testUser, Session: SessionOptions{InitSQL: []string{"SELECT 1"}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		if !readQuery(ctx, t, p, "SELECT 1") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 2) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
			return
		}
		if !serveOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestTextJSONType(t *testing.T) {
	value, err := decodeTextValue(wire.Column{Type: 245, Charset: 63}, ModeMySQL, []byte(`{"n":9007199254740993}`))
	if err != nil || value != `{"n":9007199254740993}` {
		t.Fatalf("JSON changed type or precision: %#v %v", value, err)
	}
}

func TestTextMaterializationLimits(t *testing.T) {
	const metadata = "metadata"
	for _, kind := range []string{metadata, "lob"} {
		t.Run(kind, func(t *testing.T) {
			cfg := Config{User: testUser, Limits: Limits{MaxMessageBytes: 128, MaxLOBBytes: 1}}
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				if !readQuery(context.Background(), t, p, "SELECT v") {
					return
				}
				if kind == metadata {
					name := string(bytes.Repeat([]byte{'a'}, 60))
					if !sendPackets(context.Background(), t, p, []byte{2}, textColumn(name, 253, 45, 0), textColumn(name, 253, 45, 0)) {
						return
					}
				} else {
					if !sendPackets(context.Background(), t, p, []byte{1}, textColumn("v", 252, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, 'a', 'b'}) {
						return
					}
				}
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("oversized materialization retained stream")
				}
			})
			c, err := NewConnector(cfg)
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

func TestTextNextResultUsesQueryDeadline(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "CALL slow_second()") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("a", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{254, 0, 0, 10, 0}) {
			return
		}
		// The next statement may take longer than Close's cleanup budget.
		timer := time.NewTimer(1200 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		if err := p.WriteMessage(ctx, []byte{1}); err != nil {
			return
		}
		if !sendPackets(ctx, t, p, textColumn("b", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '2'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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

func TestTextOracleSpecialFloats(t *testing.T) {
	for _, typ := range []byte{4, 5} {
		for _, s := range []string{"NAN", "INF", "-INF"} {
			v, err := decodeTextValue(wire.Column{Type: typ}, ModeOracle, []byte(s))
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

func TestTextMidRowServerError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT v") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	var server *Error
	if rows.Next() || !errors.As(rows.Err(), &server) || server.Code != 1064 {
		t.Fatal(rows.Err())
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("complete row ERR poisoned stream", err)
	}
}

func TestTextInvalidResponses(t *testing.T) {
	const unknownType = "unknown-type"
	for _, kind := range []string{unknownType, "truncated-row"} {
		t.Run(kind, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "SELECT v") {
					return
				}
				if kind == unknownType {
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 200, 63, 0)) {
						return
					}
				} else if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{3, '1'}) {
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid stream retained")
				}
			})
			defer func() { _ = db.Close() }()
			rows, err := db.QueryContext(context.Background(), "SELECT v")
			if kind == unknownType {
				if !errors.Is(err, ErrUnsupported) {
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

func TestTextColumnFlags(t *testing.T) {
	c, err := wire.DecodeColumn(textColumn("v", 8, 63, 33))
	if err != nil || c.Flags != 33 {
		t.Fatal(c, err)
	}
	r := textRows{columns: []wire.Column{c}}
	if nullable, known := r.ColumnTypeNullable(0); nullable || !known {
		t.Fatal(nullable, known)
	}
}
