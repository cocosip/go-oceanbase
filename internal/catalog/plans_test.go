package catalog

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

func TestMetadataFilters(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for kind := metadata.MetadataCatalogs; kind <= metadata.MetadataServerInfo; kind++ {
			if mode == config.ModeMySQL && (kind == metadata.MetadataSynonyms || kind == metadata.MetadataTypes || kind == metadata.MetadataTypeAttributes || kind == metadata.MetadataCollectionElements) {
				continue
			}
			plan, err := Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: kind})
			if err != nil || len(plan.Columns) == 0 || plan.SQL == "" {
				t.Fatalf("mode=%d kind=%d: %v", mode, kind, err)
			}
		}
		attack := "MiX_' OR 1=1 --"
		p, err := Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataColumns, SchemaPattern: attack, NamePattern: "a/_%", ColumnPattern: "X%"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.SQL, attack) {
			t.Fatal("filter became SQL")
		}
		if reflect.DeepEqual(p.Args, []driver.NamedValue{}) || len(p.Args) != 3 || p.Args[0].Value != attack || p.Args[1].Value != "a/_%" || p.Args[2].Value != "X%" {
			t.Fatalf("lost pattern/case: %#v", p.Args)
		}
		if _, err = Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataKind(255)}); err == nil {
			t.Fatal("unknown kind accepted")
		}
		if _, err = Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataTables, TableTypes: []string{"TABLE'); DROP TABLE t;--"}}); err == nil {
			t.Fatal("unknown table type accepted")
		}
		if _, err = Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataTables, TableTypes: []string{"TABLE", "VIEW", "SYNONYM", "MATERIALIZED VIEW", "TABLE"}}); err == nil {
			t.Fatal("unbounded table-type list accepted")
		}
	}
	for _, version := range []string{"", "5.7.25", "OceanBase 3.2.4.0", "OceanBase_CE 4.0.0.0"} {
		if _, err := Plan(config.ModeMySQL, version, metadata.MetadataFilter{Kind: metadata.MetadataRoutineParameters}); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatalf("unreliable parameter dictionary accepted %q: %v", version, err)
		}
	}
}

func FuzzMetadataFilters(f *testing.F) {
	f.Add(uint8(0), false, "APP", "x' OR 1=1 --")
	f.Add(uint8(1), true, "中文", "name/_%")
	f.Fuzz(func(t *testing.T, kind uint8, oracle bool, schema, name string) {
		if len(schema) > 4096 || len(name) > 4096 {
			t.Skip()
		}
		mode := config.ModeMySQL
		if oracle {
			mode = config.ModeOracle
		}
		kinds := []metadata.MetadataKind{metadata.MetadataColumns, metadata.MetadataForeignKeys, metadata.MetadataRoutineParameters, metadata.MetadataTypeAttributes}
		filter := metadata.MetadataFilter{Kind: kinds[int(kind)%len(kinds)], SchemaPattern: "s" + schema, NamePattern: "n" + name}
		plan, err := Plan(mode, "4.3.5.0", filter)
		if err != nil {
			return
		}
		filter.SchemaPattern = "fixed-schema"
		filter.NamePattern = "fixed-name"
		fixed, err := Plan(mode, "4.3.5.0", filter)
		if err != nil {
			t.Fatal(err)
		}
		if plan.SQL != fixed.SQL || len(plan.Args) != 2 || plan.Args[0].Value != "s"+schema || plan.Args[1].Value != "n"+name {
			t.Fatal("filter changed SQL structure or lost its bound value")
		}
	})
}

func TestRoutineCreateParameters(t *testing.T) {
	ddl := "CREATE DEFINER=`user`@`host` PROCEDURE `s`.`p`(IN `a``b` DECIMAL(12,3), /* x,) */ OUT txt ENUM('a,b','c)''d'), INOUT raw VARBINARY(24), plain INT) SQL SECURITY DEFINER BEGIN SELECT 1; END"
	got, err := ParseRoutineCreateMode(ddl, "s", "p", false)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]driver.Value{
		{metadataCatalogMySQL, "s", "p", "p", nil, "a`b", int64(1), "IN", metadataLowerDecimal, nil, int64(12), int64(3), nil, nil, nil},
		{metadataCatalogMySQL, "s", "p", "p", nil, "txt", int64(2), "OUT", metadataLowerEnum, int64(4), nil, nil, nil, nil, nil},
		{metadataCatalogMySQL, "s", "p", "p", nil, "raw", int64(3), "INOUT", "varbinary", int64(24), nil, nil, nil, nil, nil},
		{metadataCatalogMySQL, "s", "p", "p", nil, "plain", int64(4), "IN", "int", nil, int64(10), int64(0), nil, nil, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	got, err = ParseRoutineCreateMode("CREATE FUNCTION f(x VARCHAR(8)) RETURNS DECIMAL(10,2) DETERMINISTIC RETURN 1", "s", "f", false)
	if err != nil || len(got) != 2 || got[0][6] != int64(0) || got[0][7] != "RETURN" || got[0][10] != int64(10) || got[1][9] != int64(8) {
		t.Fatal(got, err)
	}
	for _, ddl := range []string{"CREATE PROCEDURE p(IN x INT", "CREATE PROCEDURE p(IN x ENUM('broken))", "CREATE PROCEDURE p(OUT INT) SELECT 1", "CREATE PROCEDURE p(x INT,) SELECT 1", "CREATE PROCEDURE p(x INT, X INT) SELECT 1", "CREATE PROCEDURE p(x INT(2,3)) SELECT 1", "CREATE FUNCTION p() RETURNS DECIMAL(3,) RETURN 1", "CREATE FUNCTION p() RETURNS UNKNOWN_TYPE RETURN 1"} {
		if _, err := ParseRoutineCreateMode(ddl, "s", "p", false); err == nil {
			t.Fatal("accepted malformed DDL", ddl)
		}
	}
	got, err = ParseRoutineCreateMode("CREATE PROCEDURE p(x ENUM('', 'a,b'), y SET('a','bc'), z BIGINT UNSIGNED) SELECT 1", "s", "p", false)
	if err != nil || len(got) != 3 || got[0][9] != int64(3) || got[1][9] != int64(4) || got[2][10] != int64(20) {
		t.Fatal(got, err)
	}
}

func TestRoutineCreateYear(t *testing.T) {
	for _, typ := range []string{"YEAR", "YEAR(4)"} {
		for _, ddl := range []string{
			"CREATE PROCEDURE p(IN y " + typ + ") SELECT y",
			"CREATE FUNCTION p() RETURNS " + typ + " RETURN 2026",
		} {
			rows, err := ParseRoutineCreateMode(ddl, "app", "p", false)
			if err != nil || len(rows) != 1 || rows[0][8] != "year" {
				t.Errorf("%s: rows=%v err=%v", ddl, rows, err)
			}
		}
	}
	for _, typ := range []string{"YEAR(2)", "YEAR(0)", "YEAR(5)", "YEAR(4,0)"} {
		if _, err := ParseRoutineCreateMode("CREATE PROCEDURE p(y "+typ+") SELECT y", "app", "p", false); !errors.Is(err, dberrors.ErrBind) {
			t.Errorf("invalid %s: %v", typ, err)
		}
	}
}

func TestMySQLMetadataTemporalScale(t *testing.T) {
	for _, tc := range []struct {
		kind  metadata.MetadataKind
		alias string
	}{{metadata.MetadataColumns, "c"}, {metadata.MetadataRoutineParameters, "p"}} {
		plan, err := Plan(config.ModeMySQL, "4.3.5.0", metadata.MetadataFilter{Kind: tc.kind})
		if err != nil {
			t.Fatal(err)
		}
		a := tc.alias
		want := "CASE WHEN " + a + ".DATA_TYPE IN ('time','datetime','timestamp') THEN " + a + ".DATETIME_PRECISION ELSE " + a + ".NUMERIC_SCALE END AS \"scale\""
		if !strings.Contains(plan.SQL, want) {
			t.Errorf("kind %d lacks temporal precision: %s", tc.kind, plan.SQL)
		}
	}
}

func TestMySQLRoutineMetadataNamespaces(t *testing.T) {
	plan, err := Plan(config.ModeMySQL, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataRoutineParameters})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "r.ROUTINE_TYPE=p.ROUTINE_TYPE") {
		t.Fatal("same-named functions and procedures cross-join: " + plan.SQL)
	}
	if strings.Contains(plan.SQL, "ROUTINE_TYPE='PROCEDURE'") {
		t.Fatal("public metadata excluded functions: " + plan.SQL)
	}
}

func TestMetadataColumnFlags(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		p, err := Plan(mode, "4.3.5.0", metadata.MetadataFilter{Kind: metadata.MetadataColumns})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Columns) != 15 || p.Columns[13].Name != "auto_increment" || p.Columns[14].Name != metadataGenerated || !p.Columns[13].Numeric || !p.Columns[14].Numeric {
			t.Fatalf("columns: %#v", p.Columns)
		}
		if !strings.Contains(p.SQL, `AS "auto_increment"`) || !strings.Contains(p.SQL, `AS "generated"`) {
			t.Fatal(p.SQL)
		}
	}
}
