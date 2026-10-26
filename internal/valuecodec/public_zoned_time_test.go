package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestZonedTimeSQLScan(t *testing.T) {
	for _, typ := range []byte{200, 201, 202} {
		for _, parse := range []bool{false, true} {
			for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
				for _, framing := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
					t.Run(fmt.Sprintf("%d/%t/%s/%s", typ, parse, path, framing), func(t *testing.T) {
						testZonedTimeSQLScan(t, typ, parse, path, framing)
					})
				}
			}
		}
	}
}

func testZonedTimeSQLScan(t *testing.T, typ byte, parse bool, path, framing string) {
	t.Helper()
	const query = "SELECT CREATED_AT FROM t"
	loc := time.UTC
	want := time.Date(2024, 2, 29, 12, 34, 56, 123456789, loc)
	data := []byte{20, 24, 2, 29, 12, 34, 56, 21, 205, 91, 7, 9}
	name, text := "TIMESTAMP", "2024-02-29 12:34:56.123456789"
	switch typ {
	case 200:
		data = append(data, 8, 0, 0, 0)
		name, text = "TIMESTAMP WITH TIME ZONE", text+" +08:00"
		want = time.Date(2024, 2, 29, 12, 34, 56, 123456789, time.FixedZone("", 8*3600))
	case 201:
		name, text = "TIMESTAMP WITH LOCAL TIME ZONE", text+" +00:00"
	}
	cfg := oceanbase.Config{Mode: oceanbase.ModeOracle, Session: oceanbase.SessionOptions{ParseTime: parse, Location: loc}}
	if path == testClientStrategy {
		cfg.PrepareMode = oceanbase.PrepareClient
	}
	db := publictest.BatchRewriteDB(t, cfg, framing, func(ctx context.Context, p *wire.PacketIO) {
		row, nullRow := []byte{}, []byte{251}
		if path == testServerStrategy {
			if !publictest.ServePrepareStatus(ctx, t, p, query, 7, 0, [][]byte{testpeer.TextColumn("CREATED_AT", typ, 63, 0)}, 6) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(7)) {
				return
			}
			row, nullRow = []byte{0, 0}, []byte{0, 4}
		} else if !testpeer.ReadQuery(ctx, t, p, query) {
			return
		}
		row = append(append(row, byte(len(data))), data...)
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("CREATED_AT", typ, 63, 0), []byte{254, 0, 0, 6, 0}, row, nullRow, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var stmt *sql.Stmt
	var rows *sql.Rows
	var err error
	if path == testTextStrategy {
		rows, err = db.QueryContext(t.Context(), query)
	} else {
		stmt, err = db.PrepareContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stmt.Close() }()
		rows, err = stmt.QueryContext(t.Context())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != 1 {
		t.Fatal(columns, err)
	}
	wantScan := reflect.TypeFor[string]()
	if parse {
		wantScan = reflect.TypeFor[time.Time]()
	}
	if columns[0].ScanType() != wantScan || columns[0].DatabaseTypeName() != name {
		t.Fatal("incorrect temporal metadata", columns[0])
	}
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	checkZonedTimeRow(t, rows, typ, parse, want, text)
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	checkZonedTimeNull(t, rows)
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if stmt != nil {
		if err := stmt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatal("session not reusable", err)
	}
}

func checkZonedTimeNull(t *testing.T, rows *sql.Rows) {
	t.Helper()
	var nullable sql.NullTime
	var ptr *time.Time
	var nullableTZ sql.Null[oceanbase.TimestampTZ]
	var nullableLTZ sql.Null[oceanbase.TimestampLTZ]
	for _, dest := range []any{&nullable, &ptr, &nullableTZ, &nullableLTZ} {
		if err := rows.Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if nullable.Valid || ptr != nil || nullableTZ.Valid || nullableLTZ.Valid {
		t.Fatal("NULL changed")
	}
}

func checkZonedTimeRow(t *testing.T, rows *sql.Rows, typ byte, parse bool, want time.Time, text string) {
	t.Helper()
	if parse {
		var instant time.Time
		var ptr *time.Time
		var nullable sql.NullTime
		var generic sql.Null[time.Time]
		for _, dest := range []any{&instant, &ptr, &nullable, &generic} {
			if err := rows.Scan(dest); err != nil {
				t.Fatal(err)
			}
		}
		for _, got := range []time.Time{instant, *ptr, nullable.Time, generic.V} {
			if !got.Equal(want) || got.Format("-07:00") != want.Format("-07:00") {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	} else {
		var got string
		if err := rows.Scan(&got); err != nil || got != text {
			t.Fatal(got, err)
		}
	}
	var tz oceanbase.TimestampTZ
	var ltz oceanbase.TimestampLTZ
	switch typ {
	case 200:
		if err := rows.Scan(&tz); err != nil {
			t.Fatal(err)
		}
		got, err := tz.Time()
		if err != nil || !got.Equal(want) {
			t.Fatal(got, err)
		}
	case 201:
		if err := rows.Scan(&ltz); err != nil {
			t.Fatal(err)
		}
		got, err := ltz.Time()
		if err != nil || !got.Equal(want) {
			t.Fatal(got, err)
		}
	}
}
