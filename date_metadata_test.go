package oceanbase

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testDateQuery = "SELECT d FROM t"
const testDateText = "2024-02-29 12:34:56"

func TestDateColumnTypes(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				for _, parseTime := range []bool{false, true} {
					t.Run(fmt.Sprintf("%d/%s/%s/%t", mode, path, framing, parseTime), func(t *testing.T) {
						testDateColumnTypes(t, mode, path, framing, parseTime)
					})
				}
			}
		}
	}
}

func testDateColumnTypes(t *testing.T, mode Mode, path, framing string, parseTime bool) {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	cfg := Config{Mode: mode, Session: SessionOptions{ParseTime: parseTime}}
	if path == testClientStrategy {
		cfg.PrepareMode = PrepareClient
	}
	db := batchRewriteDB(t, cfg, framing, func(ctx context.Context, p *wire.PacketIO) {
		row := append([]byte{byte(len(testDateText))}, testDateText...)
		nullRow := []byte{251}
		if path == testServerStrategy {
			if !servePrepareStatus(ctx, t, p, testDateQuery, 7, 0, [][]byte{textColumn("d", 12, 63, 0)}, status) || !readCommand(ctx, t, p, executeHeader(7)) {
				return
			}
			row = []byte{0, 0, 7, 0xe8, 0x07, 2, 29, 12, 34, 56}
			nullRow = []byte{0, 4}
		} else if !readQuery(ctx, t, p, testDateQuery) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("d", 12, 63, 0), []byte{254, 0, 0, status, 0}, row, nullRow, []byte{254, 0, 0, status, 0}) {
			return
		}
		if path == testServerStrategy && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var prepared *sql.Stmt
	var rows *sql.Rows
	var err error
	if path == testTextStrategy {
		rows, err = db.QueryContext(ctx, testDateQuery)
	} else {
		prepared, err = db.PrepareContext(ctx, testDateQuery)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = prepared.Close() }()
		rows, err = prepared.QueryContext(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	assertDateColumnRows(t, rows, mode, parseTime)
	if prepared != nil {
		if err = prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("date metadata disturbed session reuse", err)
	}
}

func assertDateColumnRows(t *testing.T, rows *sql.Rows, mode Mode, parseTime bool) {
	t.Helper()
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != 1 {
		t.Fatal("date column metadata missing", err)
	}
	wantType := typeNameDateTime
	if mode == ModeOracle {
		wantType = typeNameDate
	}
	if got := columns[0].DatabaseTypeName(); got != wantType {
		t.Errorf("date DatabaseTypeName=%q, want %q", got, wantType)
	}
	var want any = testDateText
	if parseTime {
		want = time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC)
	}
	if got := columns[0].ScanType(); got != reflect.TypeOf(want) {
		t.Errorf("date ScanType=%v, want %v", got, reflect.TypeOf(want))
	}
	var value any
	if !rows.Next() || rows.Scan(&value) != nil || !reflect.DeepEqual(value, want) {
		t.Fatal("date time-of-day or representation changed", value, rows.Err())
	}
	if !rows.Next() || rows.Scan(&value) != nil || value != nil {
		t.Fatal("NULL date changed", value, rows.Err())
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDateParameterMetadata(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				status := byte(2)
				wantType := typeNameDateTime
				if mode == ModeOracle {
					status, wantType = 6, typeNameDate
				}
				const query = "SELECT ? FROM dual"
				db := batchRewriteDB(t, Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
					if !readCommand(ctx, t, p, append([]byte{22}, query...)) || !sendPackets(ctx, t, p,
						[]byte{0, 7, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}, textColumn("p", 12, 63, 0), []byte{254, 0, 0, status, 0}) {
						return
					}
					if !readCommand(ctx, t, p, stmtPacket(25, 7)) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				pinned := pinnedSession(t, db)
				defer func() { _ = pinned.Close() }()
				rows, err := Metadata(context.Background(), pinned, MetadataFilter{Kind: MetadataStatementParameters, Query: query})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				values := make([]any, 11)
				pointers := make([]any, len(values))
				for i := range values {
					pointers[i] = &values[i]
				}
				if !rows.Next() || rows.Scan(pointers...) != nil {
					t.Fatal("date parameter metadata missing", rows.Err())
				}
				if values[4] != wantType || values[9] != int64(12) {
					t.Errorf("date parameter type=%v code=%v, want %s/12", values[4], values[9], wantType)
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal(rows.Err())
				}
				if err = rows.Close(); err != nil {
					t.Fatal(err)
				}
				if err = pinned.PingContext(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
