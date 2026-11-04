package catalog

import (
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/metadata"
)

// OceanBase dictionary shapes are pinned here because they differ from Oracle
// (ALL_TAB_PRIVS has TABLE_SCHEMA, not OWNER) and must not drift silently.
func TestOraclePrivilegeDictionaryPlan(t *testing.T) {
	table, err := OraclePlan(metadata.MetadataFilter{Kind: metadata.MetadataTablePrivileges})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM ALL_TAB_PRIVS p",
		`p.TABLE_SCHEMA AS "schema"`,
		`p.TABLE_NAME AS "table_name"`,
		`p.PRIVILEGE AS "privilege"`,
		"p.GRANTOR",
		"CASE p.GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END",
	} {
		if !strings.Contains(table.SQL, want) {
			t.Fatalf("table privileges SQL lost %q: %s", want, table.SQL)
		}
	}
	column, err := OraclePlan(metadata.MetadataFilter{Kind: metadata.MetadataColumnPrivileges})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM ALL_COL_PRIVS p",
		`p.OWNER AS "schema"`,
		`p.COLUMN_NAME AS "column_name"`,
		`p.PRIVILEGE AS "privilege"`,
	} {
		if !strings.Contains(column.SQL, want) {
			t.Fatalf("column privileges SQL lost %q: %s", want, column.SQL)
		}
	}
	if len(table.Columns) != 7 || len(column.Columns) != 8 {
		t.Fatalf("privilege column counts: table=%d column=%d", len(table.Columns), len(column.Columns))
	}
}

func TestMySQLPrivilegeDictionaryPlan(t *testing.T) {
	table, err := MySQLPlan("5.7.0", metadata.MetadataFilter{Kind: metadata.MetadataTablePrivileges}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM information_schema.TABLE_PRIVILEGES p",
		`p.TABLE_SCHEMA AS "schema"`,
		`p.TABLE_NAME AS "table_name"`,
		`p.PRIVILEGE_TYPE AS "privilege"`,
		"CASE p.IS_GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END",
	} {
		if !strings.Contains(table.SQL, want) {
			t.Fatalf("table privileges SQL lost %q: %s", want, table.SQL)
		}
	}
	column, err := MySQLPlan("5.7.0", metadata.MetadataFilter{Kind: metadata.MetadataColumnPrivileges}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM information_schema.COLUMN_PRIVILEGES p",
		`p.COLUMN_NAME AS "column_name"`,
		`p.PRIVILEGE_TYPE AS "privilege"`,
	} {
		if !strings.Contains(column.SQL, want) {
			t.Fatalf("column privileges SQL lost %q: %s", want, column.SQL)
		}
	}
	if len(table.Columns) != 7 || len(column.Columns) != 8 {
		t.Fatalf("privilege column counts: table=%d column=%d", len(table.Columns), len(column.Columns))
	}
}
