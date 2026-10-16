package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAllScalarPaths(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		t.Run(path, func(t *testing.T) {
			cfg := Config{User: testUser, Mode: ModeOracle, Session: SessionOptions{NCharset: testCharsetUTF16}}
			if path == testClientStrategy {
				cfg.PrepareMode = PrepareClient
			}
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 6) || !serveBaseline(ctx, t, p, 6) || !serveOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf16'"), 6) {
					return
				}
				serveScalarFixture(context.Background(), t, p, path)
			})
			connector, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			query := "SELECT scalars FROM t"
			var args []any
			if path != testTextStrategy {
				query = "SELECT ?,?,?,?,?,?,?,? FROM dual"
				args = []any{Number("1234567890123456789012345678.123456789"), NString("中😀"), Raw{0, 255}, RowID("AAAA"), TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), IntervalYM("-1-02"), IntervalDS("1 02:03:04.123456789")}
			}
			rows, err := db.QueryContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var n Number
			var ns NString
			var raw Raw
			var id RowID
			var tz TimestampTZ
			var ltz TimestampLTZ
			var ym IntervalYM
			var ds IntervalDS
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			if err = rows.Scan(&n, &ns, &raw, &id, &tz, &ltz, &ym, &ds); err != nil {
				t.Fatal(err)
			}
			if n != "1234567890123456789012345678.123456789" || ns != "中😀" || len(raw) != 2 || raw[0] != 0 || raw[1] != 255 || id != "AAAA" || tz != "2026-01-02 03:04:05.123456789 -00:30" || ltz != "2026-01-02 03:04:05.123456789 +00:00" || ym != "-1-02" || ds != "1 02:03:04.123456789" {
				t.Fatal("scalar fidelity", n, ns, raw, id, tz, ltz, ym, ds)
			}
			assertScalarNullRow(t, rows)
			if rows.Next() || rows.Err() != nil {
				t.Fatal(rows.Err())
			}
		})
	}
}

func assertScalarNullRow(t *testing.T, rows *sql.Rows) {
	t.Helper()
	var raw Raw
	var nullable sql.Null[Number]
	var nullableNS sql.Null[NString]
	var nullableID sql.Null[RowID]
	var nullableTZ sql.Null[TimestampTZ]
	var nullableLTZ sql.Null[TimestampLTZ]
	var nullableYM sql.Null[IntervalYM]
	var nullableDS sql.Null[IntervalDS]
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	if err := rows.Scan(&nullable, &nullableNS, &raw, &nullableID, &nullableTZ, &nullableLTZ, &nullableYM, &nullableDS); err != nil {
		t.Fatal(err)
	}
	if nullable.Valid || nullableNS.Valid || raw != nil || nullableID.Valid || nullableTZ.Valid || nullableLTZ.Valid || nullableYM.Valid || nullableDS.Valid {
		t.Fatal("NULL lost")
	}
}

func serveScalarFixture(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	t.Helper()
	fields := [][]byte{
		[]byte("1234567890123456789012345678.123456789"), {0x4e, 0x2d, 0xd8, 0x3d, 0xde, 0}, {0, 255}, []byte("AAAA"),
		{20, 26, 1, 2, 3, 4, 5, 21, 205, 91, 7, 9, 0, 226, 0, 0},
		{20, 26, 1, 2, 3, 4, 5, 21, 205, 91, 7, 9},
		{1, 1, 0, 0, 0, 2, 1}, {0, 1, 0, 0, 0, 2, 3, 4, 21, 205, 91, 7, 1, 9},
	}
	var row []byte
	if path == testServerStrategy {
		if !servePrepare(ctx, t, p, "SELECT ?,?,?,?,?,?,?,? FROM dual", 55, 8, nil) {
			return
		}
		payload := append(executeHeader(55), 0, 1, 246, 0, 208, 0, 203, 0, 253, 0, 200, 0, 201, 0, 204, 0, 205, 0)
		for _, data := range fields {
			payload = append(payload, byte(len(data)))
			payload = append(payload, data...)
		}
		if !readCommand(ctx, t, p, payload) {
			return
		}
		row = []byte{0, 0, 0}
	} else {
		query := "SELECT scalars FROM t"
		if path == testClientStrategy {
			query = "SELECT  1234567890123456789012345678.123456789 , UNISTR('\\4E2D\\D83D\\DE00') , HEXTORAW('00ff') , CHARTOROWID('AAAA') , TIMESTAMP '2026-01-02 03:04:05.123456789 -00:30' , CAST(TIMESTAMP '2026-01-02 03:04:05.123456789 +00:00' AS TIMESTAMP WITH LOCAL TIME ZONE) , INTERVAL '-1-02' YEAR(9) TO MONTH , INTERVAL '1 02:03:04.123456789' DAY(9) TO SECOND(9)  FROM dual"
		}
		if !readQuery(ctx, t, p, query) {
			return
		}
	}
	if !sendPackets(ctx, t, p, []byte{8}) {
		return
	}
	for _, typ := range []byte{246, 208, 203, 209, 200, 201, 204, 205} {
		if !sendPackets(ctx, t, p, textColumn("v", typ, 63, 0)) {
			return
		}
	}
	for _, data := range fields {
		row = append(row, byte(len(data)))
		row = append(row, data...)
	}
	nulls := []byte{251, 251, 251, 251, 251, 251, 251, 251}
	if path == testServerStrategy {
		nulls = []byte{0, 252, 3}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, row, nulls, []byte{254, 0, 0, 6, 0}) {
		return
	}
	if path == testServerStrategy && !readCommand(ctx, t, p, stmtPacket(25, 55)) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func TestInvalidCharsetDiscardsRows(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT bad") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 28, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 0x81}) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("malformed row connection reused")
		}
	})
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRowContext(context.Background(), "SELECT bad").Scan(&value); !errors.Is(err, charset.ErrEncoding) {
		t.Fatal(value, err)
	}
}
