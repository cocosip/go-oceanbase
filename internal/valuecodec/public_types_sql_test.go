package valuecodec_test

import (
	"context"
	"database/sql"
	"errors"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
)

func TestAllScalarPaths(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		t.Run(path, func(t *testing.T) {
			cfg := oceanbase.Config{User: publictest.TestUser, Mode: oceanbase.ModeOracle, Session: oceanbase.SessionOptions{NCharset: testCharsetUTF16}}
			if path == testClientStrategy {
				cfg.PrepareMode = oceanbase.PrepareClient
			}
			cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeBaseline(ctx, t, p, 6) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf16'"), 6) {
					return
				}
				serveScalarFixture(context.Background(), t, p, path)
			})
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			query := "SELECT scalars FROM t"
			var args []any
			if path != testTextStrategy {
				query = "SELECT ?,?,?,?,?,?,?,? FROM dual"
				args = []any{oceanbase.Number("1234567890123456789012345678.123456789"), oceanbase.NString("中😀"), oceanbase.Raw{0, 255}, oceanbase.RowID("AAAA"), oceanbase.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), oceanbase.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), oceanbase.IntervalYM("-1-02"), oceanbase.IntervalDS("1 02:03:04.123456789")}
			}
			rows, err := db.QueryContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var n oceanbase.Number
			var ns oceanbase.NString
			var raw oceanbase.Raw
			var id oceanbase.RowID
			var tz oceanbase.TimestampTZ
			var ltz oceanbase.TimestampLTZ
			var ym oceanbase.IntervalYM
			var ds oceanbase.IntervalDS
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
	var raw oceanbase.Raw
	var nullable sql.Null[oceanbase.Number]
	var nullableNS sql.Null[oceanbase.NString]
	var nullableID sql.Null[oceanbase.RowID]
	var nullableTZ sql.Null[oceanbase.TimestampTZ]
	var nullableLTZ sql.Null[oceanbase.TimestampLTZ]
	var nullableYM sql.Null[oceanbase.IntervalYM]
	var nullableDS sql.Null[oceanbase.IntervalDS]
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
		if !publictest.ServePrepare(ctx, t, p, "SELECT ?,?,?,?,?,?,?,? FROM dual", 55, 8, nil) {
			return
		}
		payload := append(publictest.ExecuteHeader(55), 0, 1, 246, 0, 208, 0, 203, 0, 253, 0, 200, 0, 201, 0, 204, 0, 205, 0)
		for _, data := range fields {
			payload = append(payload, byte(len(data)))
			payload = append(payload, data...)
		}
		if !testpeer.ReadCommand(ctx, t, p, payload) {
			return
		}
		row = []byte{0, 0, 0}
	} else {
		query := "SELECT scalars FROM t"
		if path == testClientStrategy {
			query = "SELECT  1234567890123456789012345678.123456789 , UNISTR('\\4E2D\\D83D\\DE00') , HEXTORAW('00ff') , CHARTOROWID('AAAA') , TIMESTAMP '2026-01-02 03:04:05.123456789 -00:30' , CAST(TIMESTAMP '2026-01-02 03:04:05.123456789 +00:00' AS TIMESTAMP(9) WITH LOCAL TIME ZONE) , INTERVAL '-1-02' YEAR(9) TO MONTH , INTERVAL '1 02:03:04.123456789' DAY(9) TO SECOND(9)  FROM dual"
		}
		if !testpeer.ReadQuery(ctx, t, p, query) {
			return
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{8}) {
		return
	}
	for _, typ := range []byte{246, 208, 203, 209, 200, 201, 204, 205} {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", typ, 63, 0)) {
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
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, row, nulls, []byte{254, 0, 0, 6, 0}) {
		return
	}
	if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 55)) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func TestInvalidCharsetDiscardsRows(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT bad") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 28, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 0x81}) {
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
