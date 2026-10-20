package valuecodec_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestUnsignedBigIntScanType(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		for _, framing := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
			t.Run(path+"/"+framing, func(t *testing.T) {
				testUnsignedBigIntScanType(t, path, framing)
			})
		}
	}
}

func testUnsignedBigIntScanType(t *testing.T, path, framing string) {
	t.Helper()
	const query = "SELECT u,s FROM t"
	unsigned := []uint64{0, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64}
	signed := []int64{-1, 0, math.MaxInt64, math.MinInt64}
	columns := [][]byte{testpeer.TextColumn("u", 8, 63, 32), testpeer.TextColumn("s", 8, 63, 0)}
	cfg := oceanbase.Config{Mode: oceanbase.ModeMySQL}
	if path == testClientStrategy {
		cfg.PrepareMode = oceanbase.PrepareClient
	}
	db := publictest.BatchRewriteDB(t, cfg, framing, func(ctx context.Context, p *wire.PacketIO) {
		if path == testServerStrategy {
			if !publictest.ServePrepareStatus(ctx, t, p, query, 7, 0, columns, 2) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(7)) {
				return
			}
		} else if !testpeer.ReadQuery(ctx, t, p, query) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, columns[0], columns[1], []byte{254, 0, 0, 2, 0}) {
			return
		}
		for i, u := range unsigned {
			uText, sText := strconv.FormatUint(u, 10), strconv.FormatInt(signed[i], 10)
			row := append([]byte{byte(len(uText))}, uText...)
			row = append(row, byte(len(sText)))
			row = append(row, sText...)
			if path == testServerStrategy {
				row = binary.LittleEndian.AppendUint64([]byte{0, 0}, u)
				row = binary.LittleEndian.AppendUint64(row, uint64(signed[i]))
			}
			if !testpeer.SendPackets(ctx, t, p, row) {
				return
			}
		}
		nullRow := []byte{251, 251}
		if path == testServerStrategy {
			nullRow = []byte{0, 12}
		}
		if !testpeer.SendPackets(ctx, t, p, nullRow, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var rows *sql.Rows
	var stmt *sql.Stmt
	var err error
	ctx := context.Background()
	if path == testTextStrategy {
		rows, err = db.QueryContext(ctx, query)
	} else {
		stmt, err = db.PrepareContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stmt.Close() }()
		rows, err = stmt.QueryContext(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	assertIntegerScanTypes(t, rows, unsigned, signed)
	if stmt != nil {
		if err = stmt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("integer metadata disturbed session reuse", err)
	}
}

func assertIntegerScanTypes(t *testing.T, rows *sql.Rows, unsigned []uint64, signed []int64) {
	t.Helper()
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != 2 {
		t.Fatal("integer metadata missing", err)
	}
	for i, want := range []reflect.Type{reflect.TypeFor[uint64](), reflect.TypeFor[int64]()} {
		if got := columns[i].ScanType(); got != want {
			t.Errorf("column %d ScanType=%v, want %v", i, got, want)
		}
	}
	for i, u := range unsigned {
		if !rows.Next() {
			t.Fatal("integer row missing", rows.Err())
		}
		destU, destS := reflect.New(columns[0].ScanType()), reflect.New(columns[1].ScanType())
		if err = rows.Scan(destU.Interface(), destS.Interface()); err != nil {
			t.Errorf("ScanType cannot receive integer row %d: %v", i, err)
		} else if !reflect.DeepEqual(destU.Elem().Interface(), u) || destS.Elem().Interface() != signed[i] {
			t.Errorf("integer row %d changed: %v, %v", i, destU.Elem(), destS.Elem())
		}
	}
	if !rows.Next() {
		t.Fatal("NULL integer row missing", rows.Err())
	}
	destU, destS := reflect.New(reflect.PointerTo(columns[0].ScanType())), reflect.New(reflect.PointerTo(columns[1].ScanType()))
	if err = rows.Scan(destU.Interface(), destS.Interface()); err != nil || !destU.Elem().IsNil() || !destS.Elem().IsNil() {
		t.Fatal("NULL integer changed", err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("integer result boundary changed", rows.Err())
	}
}
