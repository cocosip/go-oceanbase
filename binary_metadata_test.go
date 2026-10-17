package oceanbase

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func binaryStringColumns() [][]byte {
	var columns [][]byte
	for _, collation := range []uint16{63, 46} {
		for _, typ := range []byte{253, 15, 254} {
			columns = append(columns, textColumn(fmt.Sprintf("c%d", len(columns)), typ, collation, 128))
		}
	}
	return columns
}

func binaryStringTypeNames(mode Mode) []string {
	if mode == ModeOracle {
		return []string{typeNameVarchar2, typeNameVarchar2, typeNameChar, typeNameVarchar2, typeNameVarchar2, typeNameChar}
	}
	return []string{typeNameVarbinary, typeNameVarbinary, typeNameBinary, typeNameVarchar, typeNameVarchar, typeNameChar}
}

func TestBinaryStringColumnTypes(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, path, framing), func(t *testing.T) {
					testBinaryStringColumnTypes(t, mode, path, framing)
				})
			}
		}
	}
}

func testBinaryStringColumnTypes(t *testing.T, mode Mode, path, framing string) {
	t.Helper()
	const query = "SELECT vb,vb_old,b,v,v_old,c FROM t"
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	cfg := Config{Mode: mode}
	if path == testClientStrategy {
		cfg.PrepareMode = PrepareClient
	}
	columns := binaryStringColumns()
	db := batchRewriteDB(t, cfg, framing, func(ctx context.Context, p *wire.PacketIO) {
		if path == testServerStrategy {
			if !servePrepareStatus(ctx, t, p, query, 7, 0, columns, status) || !readCommand(ctx, t, p, executeHeader(7)) {
				return
			}
		} else if !readQuery(ctx, t, p, query) {
			return
		}
		packets := append([][]byte{{6}}, columns...)
		packets = append(packets, []byte{254, 0, 0, status, 0})
		for state := range 3 {
			var row []byte
			if path == testServerStrategy {
				row = []byte{0, 0}
				if state == 2 {
					row[1] = 252
					packets = append(packets, row)
					continue
				}
			}
			for i := range columns {
				if state == 2 {
					row = append(row, 251)
					continue
				}
				var data []byte
				if state == 0 {
					data = []byte{0, 255, 0}
					if i >= 3 {
						data = []byte("中文")
					}
				}
				row = append(row, byte(len(data)))
				row = append(row, data...)
			}
			packets = append(packets, row)
		}
		packets = append(packets, []byte{254, 0, 0, status, 0})
		if !sendPackets(ctx, t, p, packets...) {
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
	var stmt *sql.Stmt
	var rows *sql.Rows
	var err error
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
	assertBinaryStringRows(t, rows, mode)
	if stmt != nil {
		if err = stmt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("binary metadata disturbed session reuse", err)
	}
}

func assertBinaryStringRows(t *testing.T, rows *sql.Rows, mode Mode) {
	t.Helper()
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != 6 {
		t.Fatal("binary string metadata missing", err)
	}
	for i, want := range binaryStringTypeNames(mode) {
		if got := columns[i].DatabaseTypeName(); got != want {
			t.Errorf("column %d DatabaseTypeName=%s, want %s", i, got, want)
		}
		wantScan := reflect.TypeFor[string]()
		if i < 3 {
			wantScan = reflect.TypeFor[[]byte]()
			if size, known := columns[i].Length(); !known || size != 10 {
				t.Errorf("binary column %d length=%d/%t, want 10 bytes", i, size, known)
			}
		}
		if columns[i].ScanType() != wantScan {
			t.Errorf("column %d ScanType=%v, want %v", i, columns[i].ScanType(), wantScan)
		}
	}
	for state := range 3 {
		if !rows.Next() {
			t.Fatal("binary string row missing", rows.Err())
		}
		values, targets := make([]any, 6), make([]any, 6)
		for i := range values {
			targets[i] = &values[i]
		}
		if err = rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		for i, got := range values {
			var want any
			if state != 2 {
				want = ""
				if i < 3 {
					want = []byte{}
				}
				if state == 0 {
					want = "中文"
					if i < 3 {
						want = []byte{0, 255, 0}
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("state %d column %d changed: %#v, want %#v", state, i, got, want)
			}
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("binary string result boundary changed", rows.Err())
	}
}

func TestBinaryStringParameterMetadata(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				const query = "SELECT ?,?,?,?,?,? FROM dual"
				status := byte(2)
				if mode == ModeOracle {
					status = 6
				}
				db := batchRewriteDB(t, Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
					packets := append([][]byte{{0, 7, 0, 0, 0, 0, 0, 6, 0, 0, 0, 0}}, binaryStringColumns()...)
					packets = append(packets, []byte{254, 0, 0, status, 0})
					if !readCommand(ctx, t, p, append([]byte{22}, query...)) || !sendPackets(ctx, t, p, packets...) ||
						!readCommand(ctx, t, p, stmtPacket(25, 7)) || !serveOK(ctx, t, p, []byte{14}, status) {
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
				for i, want := range binaryStringTypeNames(mode) {
					values, targets := make([]any, 11), make([]any, 11)
					for j := range values {
						targets[j] = &values[j]
					}
					if !rows.Next() || rows.Scan(targets...) != nil {
						t.Fatal("binary string parameter metadata missing", rows.Err())
					}
					wantCode := []int64{253, 15, 254}[i%3]
					if values[4] != want || values[9] != wantCode {
						t.Errorf("parameter %d type=%v code=%v, want %s/%d", i, values[4], values[9], want, wantCode)
					}
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal("binary string parameter result boundary changed", rows.Err())
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
