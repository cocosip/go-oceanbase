//go:build integration

package catalog_test

import (
	sql "database/sql"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestOracleSynonymUsage creates a synonym for a user table, reads and writes
// through it, and verifies the synonym dictionary entry.
func TestOracleSynonymUsage(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(p.Name, func(t *testing.T) { integrationTestOracleSynonymUsage(t, p) })
	}
}

func integrationTestOracleSynonymUsage(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin synonym session", err)
	}
	defer func() { _ = pinned.Close() }()
	table := fmt.Sprintf("OBGSYN_%d", time.Now().UnixNano())
	synonym := table + "_S"
	if _, err = pinned.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create synonym table", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop synonym table", err)
		}
	}()
	if _, err = pinned.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		integrationtest.FailOperation(t, "seed synonym table", err)
	}
	if _, err = pinned.ExecContext(ctx, "CREATE SYNONYM "+synonym+" FOR "+table); err != nil {
		integrationtest.FailOperation(t, "create synonym", err)
	}
	defer func() {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP SYNONYM "+synonym); err != nil {
			integrationtest.FailOperation(t, "drop synonym", err)
		}
	}()
	var count int
	if err = pinned.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+synonym).Scan(&count); err != nil || count != 1 {
		t.Fatal("read through synonym", count, err)
	}
	if _, err = pinned.ExecContext(ctx, "INSERT INTO "+synonym+" VALUES (2)"); err != nil {
		integrationtest.FailOperation(t, "write through synonym", err)
	}
	if err = pinned.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 2 {
		t.Fatal("write through synonym not visible in the target table", count, err)
	}
	var schema string
	if err = pinned.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual").Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "read synonym schema", err)
	}
	integrationCheckSynonymMetadata(t, pinned, schema, synonym, table)
}

func integrationCheckSynonymMetadata(t *testing.T, pinned *sql.Conn, schema, synonym, target string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataSynonyms, NamePattern: integrationMetadataPattern(synonym)})
	if err != nil {
		integrationtest.FailOperation(t, "synonym metadata", err)
	}
	var catalog, owner, name, targetSchema, targetName, link sql.NullString
	if !rows.Next() {
		t.Fatal("synonym metadata row missing", rows.Err())
	}
	if err = rows.Scan(&catalog, &owner, &name, &targetSchema, &targetName, &link); err != nil {
		integrationtest.FailOperation(t, "scan synonym metadata", err)
	}
	if owner.String != schema || name.String != synonym || targetSchema.String != schema || targetName.String != target {
		t.Fatal("synonym metadata identity", owner, name, targetSchema, targetName)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("synonym metadata boundary", rows.Err())
	}
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close synonym metadata", err)
	}
	tableRows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataTables, SchemaPattern: integrationMetadataPattern(schema), NamePattern: integrationMetadataPattern(synonym), TableTypes: []string{"SYNONYM"}})
	if err != nil {
		integrationtest.FailOperation(t, "synonym table metadata", err)
	}
	var tableCatalog, tableSchema, tableName, tableType, remarks sql.NullString
	if !tableRows.Next() {
		t.Fatal("synonym table metadata row missing", tableRows.Err())
	}
	if err = tableRows.Scan(&tableCatalog, &tableSchema, &tableName, &tableType, &remarks); err != nil {
		integrationtest.FailOperation(t, "scan synonym table metadata", err)
	}
	if tableSchema.String != schema || tableName.String != synonym || tableType.String != "SYNONYM" {
		t.Fatal("synonym table metadata identity", tableSchema, tableName, tableType)
	}
	if tableRows.Next() || tableRows.Err() != nil {
		t.Fatal("synonym table metadata boundary", tableRows.Err())
	}
	if err = tableRows.Close(); err != nil {
		integrationtest.FailOperation(t, "close synonym table metadata", err)
	}
}

// TestOracleSynonymChain creates a view synonym and a synonym-to-synonym chain,
// then verifies reads and dictionary targets.
func TestOracleSynonymChain(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(p.Name, func(t *testing.T) { integrationTestOracleSynonymChain(t, p) })
	}
}

func integrationTestOracleSynonymChain(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin synonym chain session", err)
	}
	defer func() { _ = pinned.Close() }()
	table := fmt.Sprintf("OBGSYNC_%d", time.Now().UnixNano())
	view := table + "_V"
	viewSynonym := table + "_SV"
	first := table + "_C1"
	second := table + "_C2"
	exec := func(query string) {
		if _, err := pinned.ExecContext(ctx, query); err != nil {
			integrationtest.FailOperation(t, "create synonym chain object", err)
		}
	}
	drop := func(kind, name string) {
		if _, err := pinned.ExecContext(integrationtest.TestContext(t), "DROP "+kind+" "+name); err != nil {
			integrationtest.FailOperation(t, "drop synonym chain object", err)
		}
	}
	exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)")
	defer drop("TABLE", table)
	exec("INSERT INTO " + table + " VALUES (1)")
	exec("CREATE VIEW " + view + " AS SELECT id FROM " + table)
	defer drop("VIEW", view)
	exec("CREATE SYNONYM " + viewSynonym + " FOR " + view)
	defer drop("SYNONYM", viewSynonym)
	exec("CREATE SYNONYM " + first + " FOR " + table)
	defer drop("SYNONYM", first)
	exec("CREATE SYNONYM " + second + " FOR " + first)
	defer drop("SYNONYM", second)
	for _, query := range []string{"SELECT COUNT(*) FROM " + viewSynonym, "SELECT COUNT(*) FROM " + second} {
		var count int
		if err := pinned.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatal("read through synonym chain", query, count, err)
		}
	}
	var schema string
	if err := pinned.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual").Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "read synonym chain schema", err)
	}
	integrationCheckSynonymMetadata(t, pinned, schema, viewSynonym, view)
	integrationCheckSynonymMetadata(t, pinned, schema, second, first)
}
