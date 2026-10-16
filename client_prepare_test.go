package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testClientQuotedText = "a'\\中文"

func TestClientPrepareEncoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   Mode
		status uint16
		value  any
		want   string
	}{
		{"mysql-quote", ModeMySQL, 2, testClientQuotedText, `_utf8mb4 X'61275ce4b8ade69687'`},
		{"mysql-no-backslash", ModeMySQL, 514, testClientQuotedText, `_utf8mb4 X'61275ce4b8ade69687'`},
		{"oracle-quote", ModeOracle, 6, testClientQuotedText, `'a''\中文'`},
		{"mysql-bytes", ModeMySQL, 2, []byte{0, 39, 255}, "X'0027ff'"},
		{"oracle-bytes", ModeOracle, 6, []byte{0, 39, 255}, "HEXTORAW('0027ff')"},
		{"nil-bytes", ModeMySQL, 2, []byte(nil), "NULL"},
		{"empty-bytes", ModeMySQL, 2, []byte{}, "X''"},
		{"null", ModeOracle, 6, nil, "NULL"},
		{"uint64", ModeMySQL, 2, uint64(math.MaxUint64), testMaxUint64Text},
		{"bool", ModeOracle, 6, true, "1"},
		{"oracle-small-double", ModeOracle, 6, float64(1e-200), "1e-200d"},
		{"oracle-large-double", ModeOracle, 6, float64(1e200), "1e+200d"},
		{"mysql-double", ModeMySQL, 2, float64(0.1), "0.1e0"},
		{"date", ModeOracle, 6, time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), "TIMESTAMP '2026-01-02 03:04:05.123456'"},
		{"oracle-nul", ModeOracle, 6, "a\x00b", "('a'||CHR(0)||'b')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: Limits{MaxMessageBytes: 1024, MaxLOBBytes: 1024}}, session: &handshakeSession{mode: tc.mode, ok: wire.OK{Status: tc.status}}}
			got, err := c.interpolate(selectBindSQL, []driver.NamedValue{{Ordinal: 1, Value: tc.value}})
			if err != nil || got != "SELECT  "+tc.want+" " {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

func TestClientPrepareTokenBoundary(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4}, Limits: Limits{MaxMessageBytes: 1024}}, session: &handshakeSession{mode: ModeOracle}}
	for _, tc := range []struct {
		value any
		want  string
	}{{int64(-1), "SELECT - -1  FROM dual"}, {float64(-1), "SELECT - -1d  FROM dual"}} {
		query, err := c.interpolate("SELECT -:x FROM dual", []driver.NamedValue{{Name: "x", Value: tc.value}})
		if err != nil || query != tc.want {
			t.Fatalf("token boundary changed: %q %v", query, err)
		}
	}
	query, err := c.interpolate("SELECT :x e3 FROM dual", []driver.NamedValue{{Name: "x", Value: int64(1)}})
	if err != nil || query != "SELECT  1  e3 FROM dual" {
		t.Fatal(query, err)
	}
}

func TestClientPrepareCharset(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Charset: "latin1"}, Limits: Limits{MaxMessageBytes: 1024}}, session: &handshakeSession{mode: ModeOracle}}
	query, err := c.interpolate("SELECT ?, 'é'", []driver.NamedValue{{Value: "€"}})
	want := string([]byte{'S', 'E', 'L', 'E', 'C', 'T', ' ', ' ', '\'', 128, '\'', ' ', ',', ' ', '\'', 233, '\''})
	if err != nil || query != want {
		t.Fatalf("%x want %x: %v", query, want, err)
	}
}

func TestClientPrepareSessionChanges(t *testing.T) {
	c, err := NewConnector(Config{User: testUser, PrepareMode: PrepareClient, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !readQuery(ctx, t, p, "SELECT  _utf8mb4 X'615c62'  /* ? */") || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 2, 0, 0}) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SELECT 'a\\',  1 "), 2) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SELECT  _utf8mb4 X'615c62'  /* ? */"), 2) {
			return
		}
		if !serveOK(ctx, t, p, []byte("\x03SELECT '{fn now()}'"), 2) {
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
	s, err := db.PrepareContext(context.Background(), "SELECT ? /* ? */")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = s.ExecContext(context.Background(), `a\b`); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err = db.ExecContext(context.Background(), "SELECT 'a\\', ?", 1); err != nil {
				t.Fatal("current SQL mode ignored", err)
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(context.Background(), "SELECT '{fn now()}'"); err != nil {
		t.Fatal(err)
	}
}

func TestClientPrepareReject(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: Limits{MaxMessageBytes: 64, MaxLOBBytes: 32}}, session: &handshakeSession{mode: ModeMySQL}}
	for _, tc := range []struct {
		query string
		value any
	}{
		{`SELECT "a\", ?, "b"`, int64(1)},
		{selectBindSQL, math.Inf(1)},
		{selectBindSQL, string([]byte{255})},
		{selectBindSQL, string(make([]byte, 100))},
		{selectBindSQL, make([]byte, 33)},
	} {
		if _, err := c.interpolate(tc.query, []driver.NamedValue{{Value: tc.value}}); err == nil {
			t.Fatal("unsafe client binding accepted")
		}
	}
	if _, err := NewConnector(Config{User: testUser, PrepareMode: 99}); err == nil {
		t.Fatal("invalid prepare mode")
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/db?prepareMode=client")
	if err != nil || cfg.PrepareMode != PrepareClient {
		t.Fatal(err)
	}
	if _, err = ParseDSN("oceanbase://app@localhost/db?prepareMode=unknown"); err == nil {
		t.Fatal("invalid DSN strategy")
	}
	_, err = c.interpolate("SELECT ? /* ? */", nil)
	if !errors.Is(err, ErrBind) {
		t.Fatal(err)
	}
}
