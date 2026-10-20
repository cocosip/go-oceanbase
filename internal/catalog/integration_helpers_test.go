//go:build integration

package catalog_test

import (
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestMetadataProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin metadata connection", err)
	}
	defer func() { _ = pinned.Close() }()
	var schema string
	schemaSQL := "SELECT DATABASE()"
	if p.Mode == oceanbase.ModeOracle {
		schemaSQL = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual"
	}
	if err = pinned.QueryRowContext(ctx, schemaSQL).Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "metadata current schema", err)
	}
	parent := fmt.Sprintf("OBGM_%d", time.Now().UnixNano())
	child := parent + "_C"
	view := parent + "_V"
	definition := "id INTEGER PRIMARY KEY,amount DECIMAL(10,2),note VARCHAR(12)"
	if p.Mode == oceanbase.ModeOracle {
		definition = "id INTEGER PRIMARY KEY,amount NUMBER(10,2),note VARCHAR2(12)"
	}
	if _, err = pinned.ExecContext(ctx, "CREATE TABLE "+parent+" ("+definition+")"); err != nil {
		integrationtest.FailOperation(t, "create metadata parent", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+parent); err != nil {
			integrationtest.FailOperation(t, "drop metadata parent", err)
		}
	}()
	if _, err = pinned.ExecContext(ctx, "CREATE TABLE "+child+" (id INTEGER PRIMARY KEY,parent_id INTEGER,FOREIGN KEY (parent_id) REFERENCES "+parent+" (id))"); err != nil {
		integrationtest.FailOperation(t, "create metadata child", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+child); err != nil {
			integrationtest.FailOperation(t, "drop metadata child", err)
		}
	}()
	viewCreated := false
	t.Run("view-privilege", func(t *testing.T) { viewCreated = integrationCreateMetadataView(t, p, pinned, view, parent) })
	defer func() {
		if !viewCreated {
			return
		}
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP VIEW "+view); err != nil {
			integrationtest.FailOperation(t, "drop metadata view", err)
		}
	}()
	integrationConfigureMetadataRemarks(t, p, pinned, parent)
	base := oceanbase.MetadataFilter{SchemaPattern: integrationMetadataPattern(schema), NamePattern: integrationMetadataPattern(parent)}
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
		filter.NamePattern = integrationMetadataPattern(tt.name)
		filter.TableTypes = tt.types
		rows, err := oceanbase.Metadata(ctx, pinned, filter)
		if err != nil {
			integrationtest.FailOperation(t, "metadata objects", err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		if err = rows.Err(); err != nil {
			integrationtest.FailOperation(t, "read metadata objects", err)
		}
		if err = rows.Close(); err != nil {
			integrationtest.FailOperation(t, "close metadata objects", err)
		}
		if count != tt.want {
			t.Fatalf("metadata kind=%d rows=%d want=%d", tt.kind, count, tt.want)
		}
	}
	integrationCompareMetadataColumns(t, p, pinned, base, schema, parent)
	integrationCompareMetadataCatalogAndRemarks(t, p, pinned, base)
	integrationCheckResultColumnOrigins(t, pinned, schema, parent)
	for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataSchemas, oceanbase.MetadataIndexes, oceanbase.MetadataTablePrivileges, oceanbase.MetadataColumnPrivileges, oceanbase.MetadataRoutines, oceanbase.MetadataRoutineParameters, oceanbase.MetadataServerInfo} {
		f := oceanbase.MetadataFilter{Kind: kind}
		rows, err := oceanbase.Metadata(ctx, pinned, f)
		if kind == oceanbase.MetadataRoutineParameters && errors.Is(err, oceanbase.ErrUnsupported) {
			t.Log("parameter dictionary explicitly unavailable for this OceanBase version")
			continue
		}
		if err != nil {
			integrationtest.FailOperation(t, "metadata dictionary availability", err)
		}
		if err = rows.Close(); err != nil {
			integrationtest.FailOperation(t, "close metadata dictionary", err)
		}
	}
	if p.Mode == oceanbase.ModeOracle {
		for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataSynonyms, oceanbase.MetadataTypes, oceanbase.MetadataTypeAttributes, oceanbase.MetadataCollectionElements} {
			rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: kind})
			if err != nil {
				integrationtest.FailOperation(t, "Oracle metadata availability", err)
			}
			if err = rows.Close(); err != nil {
				integrationtest.FailOperation(t, "close Oracle metadata", err)
			}
		}
	}
}

func integrationCheckResultColumnOrigins(t *testing.T, pinned *sql.Conn, schema, table string) {
	t.Helper()
	e, err := oceanbase.Execute(integrationtest.TestContext(t), pinned, "SELECT t.id AS OB_VISIBLE FROM "+table+" t WHERE 1=0")
	if e != nil {
		defer func() { _ = e.Close() }()
	}
	if err != nil {
		integrationtest.FailOperation(t, "query result origins", err)
	}
	labels, err := e.Rows().Columns()
	if err != nil || len(labels) != 1 || labels[0] != "OB_VISIBLE" {
		t.Fatal("standard result label", labels, err)
	}
	if err = e.Close(); err != nil {
		integrationtest.FailOperation(t, "close result origins", err)
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

func integrationCreateMetadataView(t *testing.T, p integrationtest.Profile, pinned *sql.Conn, view, parent string) bool {
	t.Helper()
	if _, err := pinned.ExecContext(integrationtest.TestContext(t), "CREATE VIEW "+view+" AS SELECT id FROM "+parent); err != nil {
		if p.Mode == oceanbase.ModeOracle && integrationtest.OnlyServerErrorCode(err, 1031) {
			t.Skip("CREATE VIEW privilege unavailable; table metadata tested separately")
		}
		integrationtest.FailOperation(t, "create metadata view", err)
	}
	return true
}

func integrationMetadataPattern(s string) string {
	return strings.NewReplacer("/", "//", "_", "/_", "%", "/%").Replace(s)
}

func integrationCompareMetadataCatalogAndRemarks(t *testing.T, p integrationtest.Profile, pinned *sql.Conn, base oceanbase.MetadataFilter) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	filter := base
	filter.Kind = oceanbase.MetadataTables
	rows, err := oceanbase.Metadata(ctx, pinned, filter)
	if err != nil {
		integrationtest.FailOperation(t, "table remarks", err)
	}
	defer func() { _ = rows.Close() }()
	var catalog, schema, name, kind, remarks sql.NullString
	if !rows.Next() {
		t.Fatal("table remarks row missing", rows.Err())
	}
	if err = rows.Scan(&catalog, &schema, &name, &kind, &remarks); err != nil {
		integrationtest.FailOperation(t, "scan table remarks", err)
	}
	if !remarks.Valid || remarks.String != integrationMetadataTableRemark || catalog.Valid != (p.Mode == oceanbase.ModeMySQL) || catalog.Valid && catalog.String != integrationMetadataCatalogName {
		t.Fatal("table remarks or catalog differ from the mode/DDL")
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("table remarks response boundary", rows.Err())
	}
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close table remarks", err)
	}
	for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataTables, oceanbase.MetadataColumns} {
		for _, catalog := range []string{"", integrationMetadataCatalogName, "d%", "x' OR 1=1 --", "库/_%"} {
			filter.Kind, filter.Catalog = kind, catalog
			want := 0
			if catalog == "" || p.Mode == oceanbase.ModeMySQL && catalog == integrationMetadataCatalogName {
				want = 1
				if kind == oceanbase.MetadataColumns {
					want = 3
				}
			}
			integrationCheckMetadataCatalogCount(t, pinned, filter, want)
		}
	}
}

const integrationMetadataTableRemark = "table 备注 'quoted'"

const integrationMetadataCatalogName = "def"

func integrationCheckMetadataCatalogCount(t *testing.T, pinned *sql.Conn, filter oceanbase.MetadataFilter, want int) {
	t.Helper()
	rows, err := oceanbase.Metadata(integrationtest.TestContext(t), pinned, filter)
	if err != nil {
		integrationtest.FailOperation(t, "exact catalog filter", err)
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
		integrationtest.FailOperation(t, "close catalog filter", err)
	}
}

func integrationConfigureMetadataRemarks(t *testing.T, p integrationtest.Profile, pinned *sql.Conn, table string) {
	t.Helper()
	tableText := strings.ReplaceAll(integrationMetadataTableRemark, "'", "''")
	columnText := strings.ReplaceAll(integrationMetadataColumnRemark, "'", "''")
	commands := []string{
		"ALTER TABLE " + table + " COMMENT='" + tableText + "'",
		"ALTER TABLE " + table + " MODIFY note VARCHAR(12) COMMENT '" + columnText + "'",
	}
	if p.Mode == oceanbase.ModeOracle {
		commands = []string{
			"COMMENT ON TABLE " + table + " IS '" + tableText + "'",
			"COMMENT ON COLUMN " + table + ".note IS '" + columnText + "'",
		}
	}
	for _, command := range commands {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), command); err != nil {
			integrationtest.FailOperation(t, "set metadata remarks", err)
		}
	}
}

const integrationMetadataColumnRemark = "column 备注 'quoted'"

func integrationCompareMetadataColumns(t *testing.T, p integrationtest.Profile, pinned *sql.Conn, base oceanbase.MetadataFilter, schema, parent string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	filter := base
	filter.Kind = oceanbase.MetadataColumns
	rows, err := oceanbase.Metadata(ctx, pinned, filter)
	if err != nil {
		integrationtest.FailOperation(t, "metadata column descriptions", err)
	}
	defer func() { _ = rows.Close() }()
	names := make([]string, 0, 3)
	for rows.Next() {
		var catalog, defaultValue, remarks, owner sql.NullString
		var gotSchema, table, name, dataType string
		var ordinal int64
		var length, precision, scale, nullable, autoIncrement, generated sql.NullInt64
		if err = rows.Scan(&catalog, &gotSchema, &table, &name, &ordinal, &dataType, &length, &precision, &scale, &nullable, &defaultValue, &remarks, &owner, &autoIncrement, &generated); err != nil {
			integrationtest.FailOperation(t, "scan metadata columns", err)
		}
		autoOK := autoIncrement.Valid && autoIncrement.Int64 == 0
		if p.Mode == oceanbase.ModeOracle {
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
		if strings.EqualFold(name, "note") && (!remarks.Valid || remarks.String != integrationMetadataColumnRemark) {
			t.Fatal("column remarks differ from DDL")
		}
	}
	if err = rows.Err(); err != nil {
		integrationtest.FailOperation(t, "metadata column rows", err)
	}
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close metadata columns", err)
	}
	dictionarySQL := "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION"
	if p.Mode == oceanbase.ModeOracle {
		dictionarySQL = "SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER=? AND TABLE_NAME=? ORDER BY COLUMN_ID"
	}
	raw, err := pinned.QueryContext(ctx, dictionarySQL, schema, parent)
	if err != nil {
		integrationtest.FailOperation(t, "independent dictionary query", err)
	}
	var rawNames []string
	for raw.Next() {
		var name string
		if err = raw.Scan(&name); err != nil {
			integrationtest.FailOperation(t, "independent dictionary scan", err)
		}
		rawNames = append(rawNames, name)
	}
	if err = raw.Err(); err != nil {
		integrationtest.FailOperation(t, "independent dictionary rows", err)
	}
	if err = raw.Close(); err != nil {
		integrationtest.FailOperation(t, "close independent dictionary", err)
	}
	if strings.Join(names, ",") != strings.Join(rawNames, ",") {
		t.Fatal("metadata column order differs from dictionary")
	}
}

func integrationTestMySQLTemporalMetadata(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGTEMP_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (tm TIME(3),dt DATETIME(6),ts TIMESTAMP(2),yr YEAR(4))"); err != nil {
		integrationtest.FailOperation(t, "create temporal table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop temporal table", err)
		}
	}()
	c, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin temporal metadata", err)
	}
	defer func() { _ = c.Close() }()
	var schema string
	if err = c.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "read temporal schema", err)
	}
	rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: oceanbase.MetadataColumns, SchemaPattern: integrationMetadataPattern(schema), NamePattern: integrationMetadataPattern(table)})
	if err != nil {
		integrationtest.FailOperation(t, "temporal metadata", err)
	}
	defer func() { _ = rows.Close() }()
	for i, want := range []int64{3, 6, 2, 0} {
		if !rows.Next() {
			t.Fatal("temporal metadata row missing", i, rows.Err())
		}
		values, targets := make([]any, 15), make([]any, 15)
		for j := range targets {
			targets[j] = &values[j]
		}
		var scale sql.NullInt64
		targets[8] = &scale
		if err = rows.Scan(targets...); err != nil {
			integrationtest.FailOperation(t, "scan temporal metadata", err)
		}
		if scale.Valid != (i != 3) || scale.Int64 != want {
			t.Fatalf("column %d temporal scale=%v want=%d", i, scale, want)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("temporal metadata row boundary", rows.Err())
	}
}

func integrationTestMySQLNamedCall(t *testing.T, p integrationtest.Profile, prepare oceanbase.PrepareMode) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = prepare
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		integrationtest.FailOperation(t, "named call connector", err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	c, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin named call", err)
	}
	defer func() { _ = c.Close() }()
	name := fmt.Sprintf("OBGNAMED_%d", time.Now().UnixNano())
	if _, err = c.ExecContext(ctx, "CREATE FUNCTION "+name+"(different BIGINT) RETURNS BIGINT DETERMINISTIC RETURN different*2"); err != nil {
		integrationtest.FailOperation(t, "create same-name function", err)
	}
	defer func() {
		if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP FUNCTION "+name); err != nil {
			integrationtest.FailOperation(t, "drop named function", err)
		}
	}()
	if _, err = c.ExecContext(ctx, "CREATE PROCEDURE "+name+"(IN yr YEAR(4),OUT value BIGINT) SET value=yr"); err != nil {
		integrationtest.FailOperation(t, "create same-name procedure", err)
	}
	defer func() {
		if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP PROCEDURE IF EXISTS "+name); err != nil {
			integrationtest.FailOperation(t, "drop named procedure", err)
		}
	}()
	var value int64
	if _, err = c.ExecContext(ctx, "CALL "+name+"(?,?)", sql.Named("value", sql.Out{Dest: &value}), sql.Named("YR", 2026)); err != nil {
		integrationtest.FailOperation(t, "named YEAR call", err)
	}
	if value != 2026 {
		t.Fatal("named procedure/function namespace or argument order differs", value)
	}
	if _, err = c.ExecContext(ctx, "DROP PROCEDURE "+name); err != nil {
		integrationtest.FailOperation(t, "remove named procedure", err)
	}
	_, err = c.ExecContext(ctx, "CALL "+name+"(?)", sql.Named("different", 1))
	var server *oceanbase.Error
	if !errors.Is(err, oceanbase.ErrBind) && (!errors.As(err, &server) || server.Code != 1305) {
		t.Fatal("missing procedure resolved through same-name function", err)
	}
	if err = c.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "reuse after missing procedure", err)
	}
}
