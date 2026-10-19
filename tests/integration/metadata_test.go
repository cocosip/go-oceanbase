//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const metadataTableRemark = "table 备注 'quoted'"
const metadataColumnRemark = "column 备注 'quoted'"
const metadataCatalogName = "def"

func TestMetadataCatalog(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testMetadataProfile(t, p) })
	}
}

func testMetadataProfile(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin metadata connection", err)
	}
	defer func() { _ = pinned.Close() }()
	var schema string
	schemaSQL := "SELECT DATABASE()"
	if p.mode == oceanbase.ModeOracle {
		schemaSQL = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual"
	}
	if err = pinned.QueryRowContext(ctx, schemaSQL).Scan(&schema); err != nil {
		failOperation(t, "metadata current schema", err)
	}
	parent := fmt.Sprintf("OBGM_%d", time.Now().UnixNano())
	child := parent + "_C"
	view := parent + "_V"
	definition := "id INTEGER PRIMARY KEY,amount DECIMAL(10,2),note VARCHAR(12)"
	if p.mode == oceanbase.ModeOracle {
		definition = "id INTEGER PRIMARY KEY,amount NUMBER(10,2),note VARCHAR2(12)"
	}
	if _, err = pinned.ExecContext(ctx, "CREATE TABLE "+parent+" ("+definition+")"); err != nil {
		failOperation(t, "create metadata parent", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(testContext(t), "DROP TABLE "+parent); err != nil {
			failOperation(t, "drop metadata parent", err)
		}
	}()
	if _, err = pinned.ExecContext(ctx, "CREATE TABLE "+child+" (id INTEGER PRIMARY KEY,parent_id INTEGER,FOREIGN KEY (parent_id) REFERENCES "+parent+" (id))"); err != nil {
		failOperation(t, "create metadata child", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(testContext(t), "DROP TABLE "+child); err != nil {
			failOperation(t, "drop metadata child", err)
		}
	}()
	viewCreated := false
	t.Run("view-privilege", func(t *testing.T) { viewCreated = createMetadataView(t, p, pinned, view, parent) })
	defer func() {
		if !viewCreated {
			return
		}
		if _, err := pinned.ExecContext(testContext(t), "DROP VIEW "+view); err != nil {
			failOperation(t, "drop metadata view", err)
		}
	}()
	configureMetadataRemarks(t, p, pinned, parent)
	base := oceanbase.MetadataFilter{SchemaPattern: metadataPattern(schema), NamePattern: metadataPattern(parent)}
	for _, tt := range []struct {
		kind  oceanbase.MetadataKind
		name  string
		types []string
		want  int
	}{
		{oceanbase.MetadataTables, parent, []string{"TABLE"}, 1},
		{oceanbase.MetadataTables, view, []string{"VIEW"}, 1},
		{oceanbase.MetadataColumns, parent, nil, 3},
		{oceanbase.MetadataPrimaryKeys, parent, nil, 1},
		{oceanbase.MetadataForeignKeys, child, nil, 1},
	} {
		if tt.name == view && !viewCreated {
			continue
		}
		filter := base
		filter.Kind = tt.kind
		filter.NamePattern = metadataPattern(tt.name)
		filter.TableTypes = tt.types
		rows, err := oceanbase.Metadata(ctx, pinned, filter)
		if err != nil {
			failOperation(t, "metadata objects", err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		if err = rows.Err(); err != nil {
			failOperation(t, "read metadata objects", err)
		}
		if err = rows.Close(); err != nil {
			failOperation(t, "close metadata objects", err)
		}
		if count != tt.want {
			t.Fatalf("metadata kind=%d rows=%d want=%d", tt.kind, count, tt.want)
		}
	}
	compareMetadataColumns(t, p, pinned, base, schema, parent)
	compareMetadataCatalogAndRemarks(t, p, pinned, base)
	checkResultColumnOrigins(t, pinned, schema, parent)
	for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataSchemas, oceanbase.MetadataIndexes, oceanbase.MetadataTablePrivileges, oceanbase.MetadataColumnPrivileges, oceanbase.MetadataRoutines, oceanbase.MetadataRoutineParameters, oceanbase.MetadataServerInfo} {
		f := oceanbase.MetadataFilter{Kind: kind}
		rows, err := oceanbase.Metadata(ctx, pinned, f)
		if kind == oceanbase.MetadataRoutineParameters && errors.Is(err, oceanbase.ErrUnsupported) {
			t.Log("parameter dictionary explicitly unavailable for this OceanBase version")
			continue
		}
		if err != nil {
			failOperation(t, "metadata dictionary availability", err)
		}
		if err = rows.Close(); err != nil {
			failOperation(t, "close metadata dictionary", err)
		}
	}
	if p.mode == oceanbase.ModeOracle {
		for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataSynonyms, oceanbase.MetadataTypes, oceanbase.MetadataTypeAttributes, oceanbase.MetadataCollectionElements} {
			rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: kind})
			if err != nil {
				failOperation(t, "Oracle metadata availability", err)
			}
			if err = rows.Close(); err != nil {
				failOperation(t, "close Oracle metadata", err)
			}
		}
	}
}

func checkResultColumnOrigins(t *testing.T, pinned *sql.Conn, schema, table string) {
	t.Helper()
	e, err := oceanbase.Execute(testContext(t), pinned, "SELECT t.id AS OB_VISIBLE FROM "+table+" t WHERE 1=0")
	if e != nil {
		defer func() { _ = e.Close() }()
	}
	if err != nil {
		failOperation(t, "query result origins", err)
	}
	labels, err := e.Rows().Columns()
	if err != nil || len(labels) != 1 || labels[0] != "OB_VISIBLE" {
		t.Fatal("standard result label", labels, err)
	}
	if err = e.Close(); err != nil {
		failOperation(t, "close result origins", err)
	}
	s := e.Snapshot()
	if len(s.Results) != 1 || !s.Results[0].Complete || len(s.Results[0].Columns) != 1 {
		t.Fatal("missing result column metadata", s)
	}
	col := s.Results[0].Columns[0]
	if col.Name != labels[0] || col.Schema != "" && !strings.EqualFold(col.Schema, schema) || col.OriginalTable != "" && !strings.EqualFold(col.OriginalTable, table) || col.OriginalName != "" && !strings.EqualFold(col.OriginalName, "id") {
		t.Fatal("result origin differs from source table", col)
	}
	t.Logf("server origin fields may be empty: %+v", col)
}

func compareMetadataColumns(t *testing.T, p profile, pinned *sql.Conn, base oceanbase.MetadataFilter, schema, parent string) {
	t.Helper()
	ctx := testContext(t)
	filter := base
	filter.Kind = oceanbase.MetadataColumns
	rows, err := oceanbase.Metadata(ctx, pinned, filter)
	if err != nil {
		failOperation(t, "metadata column descriptions", err)
	}
	defer func() { _ = rows.Close() }()
	names := make([]string, 0, 3)
	for rows.Next() {
		var catalog, defaultValue, remarks, owner sql.NullString
		var gotSchema, table, name, dataType string
		var ordinal int64
		var length, precision, scale, nullable, autoIncrement, generated sql.NullInt64
		if err = rows.Scan(&catalog, &gotSchema, &table, &name, &ordinal, &dataType, &length, &precision, &scale, &nullable, &defaultValue, &remarks, &owner, &autoIncrement, &generated); err != nil {
			failOperation(t, "scan metadata columns", err)
		}
		autoOK := autoIncrement.Valid && autoIncrement.Int64 == 0
		if p.mode == oceanbase.ModeOracle {
			autoOK = !autoIncrement.Valid
		}
		if !autoOK || !generated.Valid || generated.Int64 != 0 {
			t.Fatal("ordinary column marked auto-increment or generated")
		}
		names = append(names, name)
		if strings.EqualFold(name, "amount") && (!precision.Valid || precision.Int64 != 10 || !scale.Valid || scale.Int64 != 2) {
			t.Fatal("decimal precision/scale differs from DDL")
		}
		if strings.EqualFold(name, "id") && (!nullable.Valid || nullable.Int64 != 0) {
			t.Fatal("primary key nullability differs from DDL")
		}
		if strings.EqualFold(name, "note") && (!remarks.Valid || remarks.String != metadataColumnRemark) {
			t.Fatal("column remarks differ from DDL")
		}
	}
	if err = rows.Err(); err != nil {
		failOperation(t, "metadata column rows", err)
	}
	if err = rows.Close(); err != nil {
		failOperation(t, "close metadata columns", err)
	}
	dictionarySQL := "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION"
	if p.mode == oceanbase.ModeOracle {
		dictionarySQL = "SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER=? AND TABLE_NAME=? ORDER BY COLUMN_ID"
	}
	raw, err := pinned.QueryContext(ctx, dictionarySQL, schema, parent)
	if err != nil {
		failOperation(t, "independent dictionary query", err)
	}
	var rawNames []string
	for raw.Next() {
		var name string
		if err = raw.Scan(&name); err != nil {
			failOperation(t, "independent dictionary scan", err)
		}
		rawNames = append(rawNames, name)
	}
	if err = raw.Err(); err != nil {
		failOperation(t, "independent dictionary rows", err)
	}
	if err = raw.Close(); err != nil {
		failOperation(t, "close independent dictionary", err)
	}
	if strings.Join(names, ",") != strings.Join(rawNames, ",") {
		t.Fatal("metadata column order differs from dictionary")
	}
}

func metadataPattern(s string) string {
	return strings.NewReplacer("/", "//", "_", "/_", "%", "/%").Replace(s)
}

func configureMetadataRemarks(t *testing.T, p profile, pinned *sql.Conn, table string) {
	t.Helper()
	tableText := strings.ReplaceAll(metadataTableRemark, "'", "''")
	columnText := strings.ReplaceAll(metadataColumnRemark, "'", "''")
	commands := []string{
		"ALTER TABLE " + table + " COMMENT='" + tableText + "'",
		"ALTER TABLE " + table + " MODIFY note VARCHAR(12) COMMENT '" + columnText + "'",
	}
	if p.mode == oceanbase.ModeOracle {
		commands = []string{
			"COMMENT ON TABLE " + table + " IS '" + tableText + "'",
			"COMMENT ON COLUMN " + table + ".note IS '" + columnText + "'",
		}
	}
	for _, command := range commands {
		if _, err := pinned.ExecContext(testContext(t), command); err != nil {
			failOperation(t, "set metadata remarks", err)
		}
	}
}

func compareMetadataCatalogAndRemarks(t *testing.T, p profile, pinned *sql.Conn, base oceanbase.MetadataFilter) {
	t.Helper()
	ctx := testContext(t)
	filter := base
	filter.Kind = oceanbase.MetadataTables
	rows, err := oceanbase.Metadata(ctx, pinned, filter)
	if err != nil {
		failOperation(t, "table remarks", err)
	}
	defer func() { _ = rows.Close() }()
	var catalog, schema, name, kind, remarks sql.NullString
	if !rows.Next() {
		t.Fatal("table remarks row missing", rows.Err())
	}
	if err = rows.Scan(&catalog, &schema, &name, &kind, &remarks); err != nil {
		failOperation(t, "scan table remarks", err)
	}
	if !remarks.Valid || remarks.String != metadataTableRemark || catalog.Valid != (p.mode == oceanbase.ModeMySQL) || catalog.Valid && catalog.String != metadataCatalogName {
		t.Fatal("table remarks or catalog differ from the mode/DDL")
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("table remarks response boundary", rows.Err())
	}
	if err = rows.Close(); err != nil {
		failOperation(t, "close table remarks", err)
	}
	for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataTables, oceanbase.MetadataColumns} {
		for _, catalog := range []string{"", metadataCatalogName, "d%", "x' OR 1=1 --", "库/_%"} {
			filter.Kind, filter.Catalog = kind, catalog
			want := 0
			if catalog == "" || p.mode == oceanbase.ModeMySQL && catalog == metadataCatalogName {
				want = 1
				if kind == oceanbase.MetadataColumns {
					want = 3
				}
			}
			checkMetadataCatalogCount(t, pinned, filter, want)
		}
	}
}

func checkMetadataCatalogCount(t *testing.T, pinned *sql.Conn, filter oceanbase.MetadataFilter, want int) {
	t.Helper()
	rows, err := oceanbase.Metadata(testContext(t), pinned, filter)
	if err != nil {
		failOperation(t, "exact catalog filter", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
	}
	if rows.Err() != nil || count != want {
		t.Fatalf("catalog filter kind=%d returned %d rows, want %d: %v", filter.Kind, count, want, rows.Err())
	}
	if err = rows.Close(); err != nil {
		failOperation(t, "close catalog filter", err)
	}
}

func createMetadataView(t *testing.T, p profile, pinned *sql.Conn, view, parent string) bool {
	t.Helper()
	if _, err := pinned.ExecContext(testContext(t), "CREATE VIEW "+view+" AS SELECT id FROM "+parent); err != nil {
		if p.mode == oceanbase.ModeOracle && onlyServerErrorCode(err, 1031) {
			t.Skip("CREATE VIEW privilege unavailable; table metadata tested separately")
		}
		failOperation(t, "create metadata view", err)
	}
	return true
}
