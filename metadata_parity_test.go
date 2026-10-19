package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestParityCharsetColumnLength(t *testing.T) {
	for _, tc := range []struct {
		collation uint16
		width     uint32
	}{{152, 2}, {153, 2}, {216, 4}, {217, 4}, {222, 4}} {
		got, known := columnLength(wire.Column{Type: 253, Charset: tc.collation, Length: 10 * tc.width}, ModeMySQL)
		if !known || got != 10 {
			t.Fatalf("collation %d: %d %v", tc.collation, got, known)
		}
		if _, known = columnLength(wire.Column{Type: 253, Charset: tc.collation, Length: 10*tc.width + 1}, ModeMySQL); known {
			t.Fatal("nonintegral character length")
		}
	}
}

func TestMetadataColumnFlags(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		p, err := metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: MetadataColumns})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.columns) != 15 || p.columns[13].name != "auto_increment" || p.columns[14].name != metadataGenerated || !p.columns[13].numeric || !p.columns[14].numeric {
			t.Fatalf("columns: %#v", p.columns)
		}
		if !strings.Contains(p.query, `AS "auto_increment"`) || !strings.Contains(p.query, `AS "generated"`) {
			t.Fatal(p.query)
		}
	}
}

func TestMetadataBuiltinTypeInfo(t *testing.T) {
	if MetadataStatementParameters != 17 || MetadataTypeInfo != 18 {
		t.Fatal("metadata enum numbers changed")
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			if !serveOK(ctx, t, p, []byte{14}, status) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
		pinned := pinnedSession(t, db)
		rows, err := Metadata(context.Background(), pinned, MetadataFilter{Kind: MetadataTypeInfo})
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.ColumnTypes()
		if err != nil || len(cols) != 10 || cols[0].Name() != "type_name" || cols[2].ScanType() != reflect.TypeFor[int64]() {
			t.Fatal(cols, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var name, category string
			var precision, lo, hi, radix, auto sql.NullInt64
			var prefix, suffix, params sql.NullString
			if err := rows.Scan(&name, &category, &precision, &lo, &hi, &radix, &prefix, &suffix, &params, &auto); err != nil {
				t.Fatal(err)
			}
			if found[name] || name == "REF" || category == "" {
				t.Fatal("invalid built-in type", name, category)
			}
			found[name] = true
			if name == typeNameNumber && (precision.Int64 != 38 || lo.Int64 != -84 || hi.Int64 != 127 || radix.Int64 != 10) {
				t.Fatal("NUMBER limits")
			}
			if name == typeNameDecimal && (precision.Int64 != 65 || hi.Int64 != 30) {
				t.Fatal("DECIMAL limits")
			}
		}
		if rows.Err() != nil || len(found) == 0 {
			t.Fatal(rows.Err())
		}
		_ = rows.Close()
		if _, err := Metadata(context.Background(), pinned, MetadataFilter{Kind: MetadataTypeInfo, NamePattern: "%"}); !errors.Is(err, ErrBind) {
			t.Fatal(err)
		}
		if err := pinned.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
		_ = db.Close()
	}
}

func TestRoutineMetadataFallbackPublic(t *testing.T) {
	for _, function := range []bool{false, true} {
		db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
			if !readQuery(ctx, t, p, "SHOW CREATE PROCEDURE `s_%`.`p``x`") {
				return
			}
			kind, ddl := "Create Procedure", "CREATE PROCEDURE `p``x`(IN `a_b` VARCHAR(12),OUT ignored INT) SELECT 1"
			if function {
				if !sendPackets(ctx, t, p, []byte{255, 0x19, 0x05, '#', '4', '2', '0', '0', '0', 'm', 'i', 's', 's', 'i', 'n', 'g'}) || !readQuery(ctx, t, p, "SHOW CREATE FUNCTION `s_%`.`p``x`") {
					return
				}
				kind, ddl = "Create Function", "CREATE FUNCTION `p``x`(`a_b` VARCHAR(12)) RETURNS INT RETURN 1"
			}
			fixture := metadataContractFixture{columns: "routine sql_mode definition", rows: [][]any{{"p`x", "", ddl}}}
			// SHOW CREATE uses a column label containing a space.
			if !sendPackets(ctx, t, p, []byte{3}, textColumn("Procedure", 253, 45, 0), textColumn("sql_mode", 253, 45, 0), textColumn(kind, 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
				return
			}
			var row []byte
			for _, v := range fixture.rows[0] {
				s := v.(string)
				row = append(row, byte(len(s)))
				row = append(row, s...)
			}
			if !sendPackets(ctx, t, p, row, []byte{254, 0, 0, 2, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
		pinned := pinnedSession(t, db)
		if err := pinned.Raw(func(raw any) error { raw.(*conn).session.obVersion = ""; return nil }); err != nil {
			t.Fatal(err)
		}
		rows, err := Metadata(context.Background(), pinned, MetadataFilter{Kind: MetadataRoutineParameters, SchemaPattern: "s/_/%", NamePattern: "p`x", ColumnPattern: "a/_b"})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		for rows.Next() {
			values := make([]any, 15)
			dest := make([]any, 15)
			for i := range dest {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			if values[1] != "s_%" || values[5] != "a_b" || values[6] != int64(1) || values[9] != int64(12) {
				t.Fatal(values)
			}
			count++
		}
		if count != 1 || rows.Err() != nil {
			t.Fatal(count, rows.Err())
		}
		_ = rows.Close()
		if err := pinned.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
		_ = db.Close()
	}
}

func TestRoutineMetadataFallbackErrors(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SHOW CREATE PROCEDURE `s`.`p`") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 0x76, 0x04, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	pinned := pinnedSession(t, db)
	_ = pinned.Raw(func(raw any) error { raw.(*conn).session.obVersion = testRoutineOldVersion; return nil })
	for _, f := range []MetadataFilter{{Kind: MetadataRoutineParameters}, {Kind: MetadataRoutineParameters, SchemaPattern: "s%", NamePattern: "p"}, {Kind: MetadataRoutineParameters, SchemaPattern: "s", NamePattern: "p_"}} {
		if _, err := Metadata(context.Background(), pinned, f); !errors.Is(err, ErrUnsupported) {
			t.Fatal("ambiguous fallback accepted", err)
		}
	}
	_, err := Metadata(context.Background(), pinned, MetadataFilter{Kind: MetadataRoutineParameters, SchemaPattern: "s", NamePattern: "p"})
	var server *Error
	if !errors.As(err, &server) || server.Code != 1142 {
		t.Fatal(err)
	}
	if err := pinned.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
	_ = db.Close()
}

func TestRoutineCreateParameters(t *testing.T) {
	ddl := "CREATE DEFINER=`user`@`host` PROCEDURE `s`.`p`(IN `a``b` DECIMAL(12,3), /* x,) */ OUT txt ENUM('a,b','c)''d'), INOUT raw VARBINARY(24), plain INT) SQL SECURITY DEFINER BEGIN SELECT 1; END"
	got, err := parseRoutineCreateMode(ddl, "s", "p", false)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]driver.Value{
		{executionColumnCatalog, "s", "p", "p", nil, "a`b", int64(1), "IN", metadataLowerDecimal, nil, int64(12), int64(3), nil, nil, nil},
		{executionColumnCatalog, "s", "p", "p", nil, "txt", int64(2), routineDirectionOut, metadataLowerEnum, int64(4), nil, nil, nil, nil, nil},
		{executionColumnCatalog, "s", "p", "p", nil, "raw", int64(3), routineDirectionInOut, "varbinary", int64(24), nil, nil, nil, nil, nil},
		{executionColumnCatalog, "s", "p", "p", nil, testCursorEpochPlain, int64(4), "IN", "int", nil, int64(10), int64(0), nil, nil, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	got, err = parseRoutineCreateMode("CREATE FUNCTION f(x VARCHAR(8)) RETURNS DECIMAL(10,2) DETERMINISTIC RETURN 1", "s", "f", false)
	if err != nil || len(got) != 2 || got[0][6] != int64(0) || got[0][7] != "RETURN" || got[0][10] != int64(10) || got[1][9] != int64(8) {
		t.Fatal(got, err)
	}
	for _, ddl := range []string{"CREATE PROCEDURE p(IN x INT", "CREATE PROCEDURE p(IN x ENUM('broken))", "CREATE PROCEDURE p(OUT INT) SELECT 1", "CREATE PROCEDURE p(x INT,) SELECT 1", "CREATE PROCEDURE p(x INT, X INT) SELECT 1", "CREATE PROCEDURE p(x INT(2,3)) SELECT 1", "CREATE FUNCTION p() RETURNS DECIMAL(3,) RETURN 1", "CREATE FUNCTION p() RETURNS UNKNOWN_TYPE RETURN 1"} {
		if _, err := parseRoutineCreateMode(ddl, "s", "p", false); err == nil {
			t.Fatal("accepted malformed DDL", ddl)
		}
	}
	got, err = parseRoutineCreateMode("CREATE PROCEDURE p(x ENUM('', 'a,b'), y SET('a','bc'), z BIGINT UNSIGNED) SELECT 1", "s", "p", false)
	if err != nil || len(got) != 3 || got[0][9] != int64(3) || got[1][9] != int64(4) || got[2][10] != int64(20) {
		t.Fatal(got, err)
	}
}

func TestMetadataBufferedBudgets(t *testing.T) {
	c := &conn{cfg: Config{Limits: Limits{MaxMessageBytes: 1 << 20, MaxResultItems: 1}}}
	if _, err := c.bufferedMetadataRows(context.Background(), MetadataTypeInfo, builtinTypeInfo(ModeMySQL)); err != nil {
		t.Fatal(err)
	}
	c.cfg.Limits.MaxResultItems = 0
	if _, err := c.bufferedMetadataRows(context.Background(), MetadataTypeInfo, nil); err == nil {
		t.Fatal("missing result budget accepted")
	}
	c.cfg.Limits.MaxResultItems = 1
	c.cfg.Limits.MaxMessageBytes = 1
	if _, err := c.bufferedMetadataRows(context.Background(), MetadataTypeInfo, nil); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	c.cfg.Limits.MaxMessageBytes = 1 << 20
	if _, err := c.bufferedMetadataRows(context.Background(), MetadataRoutineParameters, make([][]driver.Value, maxTextColumns+1)); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.bufferedMetadataRows(ctx, MetadataTypeInfo, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRoutineCreateYear(t *testing.T) {
	for _, typ := range []string{"YEAR", "YEAR(4)"} {
		for _, ddl := range []string{
			"CREATE PROCEDURE p(IN y " + typ + ") SELECT y",
			"CREATE FUNCTION p() RETURNS " + typ + " RETURN 2026",
		} {
			rows, err := parseRoutineCreateMode(ddl, "app", "p", false)
			if err != nil || len(rows) != 1 || rows[0][8] != "year" {
				t.Errorf("%s: rows=%v err=%v", ddl, rows, err)
			}
		}
	}
	for _, typ := range []string{"YEAR(2)", "YEAR(0)", "YEAR(5)", "YEAR(4,0)"} {
		if _, err := parseRoutineCreateMode("CREATE PROCEDURE p(y "+typ+") SELECT y", "app", "p", false); !errors.Is(err, ErrBind) {
			t.Errorf("invalid %s: %v", typ, err)
		}
	}
}

func TestMySQLMetadataTemporalScale(t *testing.T) {
	for _, tc := range []struct {
		kind  MetadataKind
		alias string
	}{{MetadataColumns, "c"}, {MetadataRoutineParameters, "p"}} {
		plan, err := metadataPlan(ModeMySQL, "4.3.5.0", MetadataFilter{Kind: tc.kind})
		if err != nil {
			t.Fatal(err)
		}
		a := tc.alias
		want := "CASE WHEN " + a + ".DATA_TYPE IN ('time','datetime','timestamp') THEN " + a + ".DATETIME_PRECISION ELSE " + a + ".NUMERIC_SCALE END AS \"scale\""
		if !strings.Contains(plan.query, want) {
			t.Errorf("kind %d lacks temporal precision: %s", tc.kind, plan.query)
		}
	}
}

func TestMySQLRoutineMetadataNamespaces(t *testing.T) {
	plan, err := metadataPlan(ModeMySQL, "4.3.5.0", MetadataFilter{Kind: MetadataRoutineParameters})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.query, "r.ROUTINE_TYPE=p.ROUTINE_TYPE") {
		t.Fatal("same-named functions and procedures cross-join: " + plan.query)
	}
	if strings.Contains(plan.query, "ROUTINE_TYPE='PROCEDURE'") {
		t.Fatal("public metadata excluded functions: " + plan.query)
	}
}
