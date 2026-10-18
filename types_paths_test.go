package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testClientStrategy = "client"
const testServerStrategy = "server"
const testTextStrategy = "text"
const testCharsetUTF16 = "utf16"

func TestScalarSQLCharset(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		t.Run(path, func(t *testing.T) {
			cfg := Config{User: testUser, Session: SessionOptions{Charset: "gbk"}}
			if path == testClientStrategy {
				cfg.PrepareMode = PrepareClient
			}
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte("\x03SET NAMES gbk"), 2) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) || !serveVersion(ctx, t, p, 2, false) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
					return
				}
				ctx = context.Background()
				query := "SELECT '\xd6\xd0'"
				row := []byte{2, 0xd6, 0xd0}
				if path == testServerStrategy {
					if !servePrepare(ctx, t, p, query+",?", 33, 1, nil) || !readCommand(ctx, t, p, append(executeHeader(33), 0, 1, 253, 0, 2, 0xd6, 0xd0)) {
						return
					}
					row = append([]byte{0, 0}, row...)
				} else {
					if path == testClientStrategy {
						query += ", _gbk X'd6d0' "
					}
					if !readQuery(ctx, t, p, query) {
						return
					}
				}
				if !sendPackets(ctx, t, p, []byte{1}, textColumn("\xd6\xd0", 253, 28, 0), []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
					return
				}
				if path == testServerStrategy && !readCommand(ctx, t, p, stmtPacket(25, 33)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			connector, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			query := "SELECT '中'"
			var args []any
			if path != testTextStrategy {
				query += ",?"
				args = []any{"中"}
			}
			rows, err := db.QueryContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			names, err := rows.Columns()
			if err != nil || len(names) != 1 || names[0] != "中" {
				t.Fatal("metadata encoding", names, err)
			}
			var value string
			if !rows.Next() || rows.Scan(&value) != nil || value != "中" {
				t.Fatal("value encoding", value, rows.Err())
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOracleParseTime(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{ParseTime: true, Location: time.UTC}}, session: &handshakeSession{mode: ModeOracle}}
	data := []byte{20, 24, 2, 29, 12, 34, 56, 21, 205, 91, 7, 9}
	want := time.Date(2024, 2, 29, 12, 34, 56, 123456789, time.UTC)
	for _, binaryRows := range []bool{false, true} {
		value, err := c.decodeRowValue(wire.Column{Type: 202}, binaryRows, data)
		if err != nil || value != want {
			t.Fatal("Oracle TIMESTAMP ignored parseTime", value, err)
		}
	}
	rows := &textRows{conn: c, columns: []wire.Column{{Type: 202}, {Type: 201}}}
	if rows.ColumnTypeScanType(0) != reflect.TypeFor[time.Time]() || rows.ColumnTypeScanType(1) != reflect.TypeFor[string]() {
		t.Fatal("parseTime scan metadata disagrees with values")
	}
}

func TestScalarSQLRejectsInvalidBeforeSend(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	for _, query := range []string{"SELECT '\x00'", "SELECT '\xff'"} {
		if _, err := db.ExecContext(context.Background(), query); err == nil {
			t.Fatal("invalid SQL sent", query)
		}
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScalarBinaryNullEmpty(t *testing.T) {
	var raw Raw
	if err := raw.Scan([]byte{}); err != nil || raw == nil {
		t.Fatal(raw, err)
	}
	source := []byte{0, 255}
	if err := raw.Scan(source); err != nil {
		t.Fatal(err)
	}
	source[0] = 1
	if !bytes.Equal(raw, []byte{0, 255}) {
		t.Fatal("RAW aliases row buffer")
	}
}

func TestScalarClientLiteralQuoting(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: Limits{MaxLOBBytes: 1024}}, session: &handshakeSession{mode: ModeOracle}}
	// A value that would end its literal early must either be rejected by the
	// type's validation or have every embedded quote doubled.
	for _, value := range []any{
		RowID("AAAA')||CHR(0)||('"),
		IntervalYM("1-2'||'"),
		IntervalDS("1 00:00:00'||'"),
		TimestampTZ("2026-01-02 03:04:05 +00:00'"),
		TimestampLTZ("2026-01-02 03:04:05 +00:00'"),
	} {
		text, err := c.clientLiteral(value, 1024)
		if err != nil {
			continue
		}
		if quotes := strings.Count(text, "'"); quotes%2 != 0 {
			t.Fatalf("%T produced an unbalanced literal: %s", value, text)
		}
	}
}

func TestScalarClientLiterals(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Charset: testUTF8MB4, Location: time.UTC}, Limits: Limits{MaxLOBBytes: 1024}}, session: &handshakeSession{mode: ModeOracle}}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{NString("中😀'"), "UNISTR('\\4E2D\\D83D\\DE00\\0027')"},
		{Raw{0, 255}, "HEXTORAW('00ff')"}, {RowID("AAAA"), "CHARTOROWID('AAAA')"},
		{TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), "TIMESTAMP '2026-01-02 03:04:05.123456789 -00:30'"},
		{TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), "CAST(TIMESTAMP '2026-01-02 03:04:05.123456789 +00:00' AS TIMESTAMP WITH LOCAL TIME ZONE)"},
		{IntervalYM("-1-02"), "INTERVAL '-1-02' YEAR(9) TO MONTH"},
		{IntervalDS("1 02:03:04.123456789"), "INTERVAL '1 02:03:04.123456789' DAY(9) TO SECOND(9)"},
	} {
		text, err := c.clientLiteral(tc.value, 1024)
		if err != nil || text != tc.want {
			t.Fatal(tc.value, text, err)
		}
		if _, err = c.clientLiteral(tc.value, 2); err == nil {
			t.Fatal("expanded literal escaped limit", tc.value)
		}
	}
}

func TestTypeCharsetBoundaries(t *testing.T) {
	for _, binaryRows := range []bool{false, true} {
		c := &conn{cfg: Config{Session: SessionOptions{Charset: "latin1", NCharset: testCharsetUTF16}}, session: &handshakeSession{mode: ModeOracle}}
		v, err := c.decodeRowValue(wire.Column{Type: 208, Charset: 63}, binaryRows, []byte{0x4e, 0x2d})
		if err != nil || v != "中" {
			t.Fatal("NCHAR treated as binary or connection charset", v, err)
		}
		v, err = c.decodeRowValue(wire.Column{Type: 209, Charset: 63}, binaryRows, []byte("AAAA"))
		if err != nil || v != "AAAA" {
			t.Fatal("ROWID treated as binary", v, err)
		}
		v, err = c.decodeRowValue(wire.Column{Type: 253, Charset: 28}, binaryRows, []byte{0xd6, 0xd0})
		if err != nil || v != "中" {
			t.Fatal(v, err)
		}
	}
	c := &conn{cfg: Config{Session: SessionOptions{ParseTime: true, Location: time.UTC}}, session: &handshakeSession{mode: ModeMySQL}}
	v, err := c.decodeRowValue(wire.Column{Type: 10}, false, []byte("2024-02-29"))
	if err != nil || v != time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC) {
		t.Fatal(v, err)
	}
	if _, err = c.decodeRowValue(wire.Column{Type: 10}, false, []byte("0000-00-00")); !errors.Is(err, ErrType) {
		t.Fatal("zero date fabricated as time.Time", err)
	}
	v, err = c.decodeRowValue(wire.Column{Type: 11}, false, []byte("-838:59:59.999999"))
	if err != nil || v != "-838:59:59.999999" {
		t.Fatal("TIME lost duration semantics", v, err)
	}
}

func TestTypeSessionInitialization(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Session: SessionOptions{Location: time.FixedZone("business", 8*3600), NCharset: testCharsetUTF16, InitSQL: []string{"SET SESSION time_zone = '-03:00'"}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) || !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 6) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) || !serveVersion(ctx, t, p, 6, false) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+08:00'"), 6) || !serveOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf16'"), 6) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '-03:00'"), 6) {
			return
		}
		for _, query := range []string{"SET NAMES utf8mb4", "SET SESSION time_zone = '+08:00'", "SET NCHARACTER_SET_CONNECTION = 'utf16'"} {
			if !serveOK(ctx, t, p, append([]byte{3}, query...), 6) {
				return
			}
		}
		if !serveOK(context.Background(), t, p, []byte{14}, 6) {
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
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScalarClientCharsetRestrictions(t *testing.T) {
	if _, err := handshakeCharset(testCharsetUTF16); err == nil {
		t.Fatal("UTF16 cannot be used as a SQL client charset")
	}
	if _, err := NewConnector(Config{User: testUser, Session: SessionOptions{NCharset: "gb18030_2022"}}); err == nil {
		t.Fatal("GB18030-2022 silently aliased to older mapping")
	}
}

func TestScalarBinaryMaterializationLimit(t *testing.T) {
	cfg := Config{User: testUser, Mode: ModeOracle, Limits: Limits{MaxLOBBytes: 2}}
	cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) || !serveBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !servePrepare(ctx, t, p, "INSERT INTO t VALUES (?)", 44, 1, nil) || !readCommand(ctx, t, p, stmtPacket(25, 44)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	stmt, err := db.PrepareContext(context.Background(), "INSERT INTO t VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	for _, value := range []any{Raw{1, 2, 3}, []byte{1, 2, 3}} {
		if _, err = stmt.ExecContext(context.Background(), value); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("binary argument escaped MaxLOBBytes", err)
		}
	}
}
