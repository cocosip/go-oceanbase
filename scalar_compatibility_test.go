package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const scalarCompatibilityQuery = "SELECT tiny,utiny,yr,b1,b64 FROM t"

type mysqlScalarFixture struct {
	tiny, unsigned, year int64
	bit                  byte
	bits                 []byte
}

func TestMySQLScalarCompatibility(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(path+"/"+framing, func(t *testing.T) {
				testMySQLScalarCompatibility(t, path, framing)
			})
		}
	}
}

func testMySQLScalarCompatibility(t *testing.T, path, framing string) {
	t.Helper()
	fixtures := []mysqlScalarFixture{
		{-128, 0, 0, 0, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
		{0, 1, 1901, 1, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
		{1, 128, 2026, 0, []byte{128, 0, 0, 0, 0, 0, 0, 0}},
		{127, 255, 2155, 1, []byte{255, 255, 255, 255, 255, 255, 255, 255}},
	}
	columns := [][]byte{
		textColumn("tiny", 1, 63, 0), textColumn("utiny", 1, 63, 32),
		textColumn("yr", 13, 63, 32), textColumn("b1", 16, 63, 32), textColumn("b64", 16, 63, 32),
	}
	for i, width := range []uint32{1, 3, 4, 1, 64} {
		binary.LittleEndian.PutUint32(columns[i][len(columns[i])-10:], width)
	}
	cfg := Config{Mode: ModeMySQL}
	if path == testClientStrategy {
		cfg.PrepareMode = PrepareClient
	}
	db := batchRewriteDB(t, cfg, framing, func(ctx context.Context, p *wire.PacketIO) {
		serveMySQLScalarCompatibility(ctx, t, p, path, columns, fixtures)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var rows *sql.Rows
	var statement *sql.Stmt
	var err error
	if path == testTextStrategy {
		rows, err = db.QueryContext(ctx, scalarCompatibilityQuery)
	} else {
		statement, err = db.PrepareContext(ctx, scalarCompatibilityQuery)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = statement.Close() }()
		rows, err = statement.QueryContext(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	assertMySQLScalarCompatibility(t, rows, fixtures)
	if statement != nil {
		if err = statement.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("scalar decoding disturbed connection reuse", err)
	}
}

func serveMySQLScalarCompatibility(ctx context.Context, t *testing.T, p *wire.PacketIO, path string, columns [][]byte, fixtures []mysqlScalarFixture) {
	t.Helper()
	if path == testServerStrategy {
		if !servePrepareStatus(ctx, t, p, scalarCompatibilityQuery, 7, 0, columns, 2) || !readCommand(ctx, t, p, executeHeader(7)) {
			return
		}
	} else if !readQuery(ctx, t, p, scalarCompatibilityQuery) {
		return
	}
	if !sendPackets(ctx, t, p, []byte{5}) || !sendPackets(ctx, t, p, columns...) || !sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}) {
		return
	}
	for _, fixture := range fixtures {
		var row []byte
		if path == testServerStrategy {
			row = []byte{0, 0, byte(fixture.tiny), byte(fixture.unsigned)}
			row = binary.LittleEndian.AppendUint16(row, uint16(fixture.year))
		} else {
			for _, number := range []int64{fixture.tiny, fixture.unsigned, fixture.year} {
				text := strconv.FormatInt(number, 10)
				row = append(row, byte(len(text)))
				row = append(row, text...)
			}
		}
		row = append(row, 1, fixture.bit, byte(len(fixture.bits)))
		row = append(row, fixture.bits...)
		if !sendPackets(ctx, t, p, row) {
			return
		}
	}
	nulls := []byte{251, 251, 251, 251, 251}
	if path == testServerStrategy {
		nulls = []byte{0, 124}
	}
	if !sendPackets(ctx, t, p, nulls, []byte{254, 0, 0, 2, 0}) {
		return
	}
	if path == testServerStrategy && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
		return
	}
	if !serveOK(ctx, t, p, []byte{14}, 2) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func assertMySQLScalarCompatibility(t *testing.T, rows *sql.Rows, fixtures []mysqlScalarFixture) {
	t.Helper()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != 5 {
		t.Fatal("scalar metadata missing", err)
	}
	for i, want := range []string{"TINYINT", "TINYINT", "YEAR", "BIT", "BIT"} {
		scanType := reflect.TypeFor[int64]()
		if i >= 3 {
			scanType = reflect.TypeFor[[]byte]()
		}
		if columns[i].DatabaseTypeName() != want || columns[i].ScanType() != scanType {
			t.Errorf("column %d: type=%s scan=%v", i, columns[i].DatabaseTypeName(), columns[i].ScanType())
		}
	}
	var retained []byte
	for _, fixture := range fixtures {
		if !rows.Next() {
			t.Fatal("scalar row missing", rows.Err())
		}
		var tiny, unsigned, year, bit, bits any
		if err = rows.Scan(&tiny, &unsigned, &year, &bit, &bits); err != nil {
			t.Fatal(err)
		}
		got := []any{tiny, unsigned, year, bit, bits}
		want := []any{fixture.tiny, fixture.unsigned, fixture.year, []byte{fixture.bit}, fixture.bits}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fixed Go scalar contract changed: got %#v, want %#v", got, want)
		}
		if retained == nil {
			retained = bits.([]byte)
		}
	}
	var nulls [5]any
	if !rows.Next() {
		t.Fatal("NULL scalar row missing", rows.Err())
	}
	if err = rows.Scan(&nulls[0], &nulls[1], &nulls[2], &nulls[3], &nulls[4]); err != nil || !reflect.DeepEqual(nulls, [5]any{}) {
		t.Fatal("NULL scalar identity changed", err)
	}
	if rows.Next() || rows.Err() != nil || !reflect.DeepEqual(retained, fixtures[0].bits) {
		t.Fatal("scalar response boundary or retained bytes changed", rows.Err())
	}
}
