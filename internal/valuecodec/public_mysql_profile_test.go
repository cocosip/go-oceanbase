package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const mysqlProfileQuery = "SELECT body,tm,dt_zero,dt_fraction FROM t"
const mysqlProfileFraction = "2024-01-01 12:00:00.0100"

func TestMySQLClientProfileRows(t *testing.T) {
	for _, version := range []int{0, 5, 8} {
		for _, newMetadata := range []bool{false, true} {
			for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
				t.Run(fmt.Sprintf("%d/metadata=%t/%s", version, newMetadata, path), func(t *testing.T) {
					testMySQLClientProfileRows(t, version, newMetadata, path)
				})
			}
		}
	}
}

func testMySQLClientProfileRows(t *testing.T, version int, newMetadata bool, path string) {
	t.Helper()
	columns := [][]byte{
		testpeer.TextColumn("body", 250, 45, 0),
		testpeer.TextColumn("tm", 11, 63, 0),
		testpeer.TextColumn("dt_zero", 12, 63, 0),
		testpeer.TextColumn("dt_fraction", 12, 63, 0),
	}
	for _, column := range columns[1:] {
		column[len(column)-3] = 4
	}
	cfg := oceanbase.Config{User: testpeer.User, Mode: oceanbase.ModeMySQL,
		Session:  oceanbase.SessionOptions{CompatibleMysqlVersion: version},
		Protocol: oceanbase.ProtocolOptions{Compression: oceanbase.FeatureOff, OB20: oceanbase.FeatureOff, NewMetadata: oceanbase.FeatureOff}}
	if newMetadata {
		cfg.Protocol.NewMetadata = oceanbase.FeatureRequired
	}
	if path == testClientStrategy {
		cfg.PrepareMode = oceanbase.PrepareClient
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(false)) || !testpeer.ReadAuthResponse(ctx, t, p) {
			return
		}
		var variables map[string]string
		if newMetadata {
			variables = map[string]string{"__ob_client_capability_flag": "2"}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, variables)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		serveMySQLClientProfileRows(context.Background(), t, p, path, columns)
	})
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	var stmt *sql.Stmt
	var rows *sql.Rows
	if path == testTextStrategy {
		rows, err = db.QueryContext(t.Context(), mysqlProfileQuery)
	} else {
		stmt, err = db.PrepareContext(t.Context(), mysqlProfileQuery)
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
	assertMySQLClientProfileRows(t, rows, version, newMetadata, path)
	if stmt != nil {
		if err = stmt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.PingContext(t.Context()); err != nil {
		t.Fatal("profile decoding disturbed session reuse", err)
	}
}

func assertMySQLClientProfileRows(t *testing.T, rows *sql.Rows, version int, newMetadata bool, path string) {
	t.Helper()
	types, err := rows.ColumnTypes()
	if err != nil || len(types) != 4 {
		t.Fatal("profile column metadata missing", err)
	}
	wantBody := "VARCHAR"
	if version == 8 && newMetadata {
		wantBody = "TEXT"
	}
	for i, name := range []string{wantBody, "TIME", valueformat.TypeNameDateTime, valueformat.TypeNameDateTime} {
		if types[i].DatabaseTypeName() != name || types[i].ScanType() != reflect.TypeFor[string]() {
			t.Errorf("column %d: type=%q scan=%v, want %q/string", i, types[i].DatabaseTypeName(), types[i].ScanType(), name)
		}
	}
	want := [4]any{"text", "12:34:56", "2024-01-01 12:00:00.0000", mysqlProfileFraction}
	if path == testServerStrategy {
		want[2], want[3] = "2024-01-01 12:00:00.000000", "2024-01-01 12:00:00.010000"
	}
	if version == 8 {
		want[1], want[2], want[3] = "12:34:56.1000", "2024-01-01 12:00:00", mysqlProfileFraction
	}
	for _, expected := range [][4]any{want, {}} {
		var got [4]any
		if !rows.Next() {
			t.Fatal("profile row missing", rows.Err())
		}
		if err = rows.Scan(&got[0], &got[1], &got[2], &got[3]); err != nil || got != expected {
			t.Fatalf("profile values: got %#v (%v), want %#v", got, err, expected)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("profile result boundary changed", rows.Err())
	}
}

func serveMySQLClientProfileRows(ctx context.Context, t *testing.T, p *wire.PacketIO, path string, columns [][]byte) {
	t.Helper()
	if path == testServerStrategy {
		if !publictest.ServePrepareStatus(ctx, t, p, mysqlProfileQuery, 7, 0, columns, 2) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(7)) {
			return
		}
	} else if !testpeer.ReadQuery(ctx, t, p, mysqlProfileQuery) {
		return
	}
	var row []byte
	nulls := []byte{251, 251, 251, 251}
	if path == testServerStrategy {
		row = []byte{0, 0, 4, 't', 'e', 'x', 't',
			12, 0, 0, 0, 0, 0, 12, 34, 56, 0xa0, 0x86, 1, 0,
			11, 0xe8, 7, 1, 1, 12, 0, 0, 0, 0, 0, 0,
			11, 0xe8, 7, 1, 1, 12, 0, 0, 0x10, 0x27, 0, 0}
		nulls = []byte{0, 60}
	} else {
		for _, value := range []string{"text", "12:34:56.1000", "2024-01-01 12:00:00.0000", mysqlProfileFraction} {
			row = append(row, byte(len(value)))
			row = append(row, value...)
		}
	}
	packets := append([][]byte{{4}}, columns...)
	packets = append(packets, []byte{254, 0, 0, 2, 0}, row, nulls, []byte{254, 0, 0, 2, 0})
	if !testpeer.SendPackets(ctx, t, p, packets...) {
		return
	}
	if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
		return
	}
	if testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	}
}
