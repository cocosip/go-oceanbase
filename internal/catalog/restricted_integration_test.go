//go:build integration

package catalog_test

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestMetadataRestrictedVisibility verifies that a least-privilege identity sees
// only the dictionary rows the server exposes to it, and that the driver does not
// fabricate results for schemas it cannot read.
func TestMetadataRestrictedVisibility(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestRestrictedVisibility(t, p) })
	}
}

func integrationTestRestrictedVisibility(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	key := strings.TrimSuffix(p.Env, "_DSN") + "_RESTRICTED_DSN"
	dsn := os.Getenv(key)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required restricted identity missing: " + p.Name)
		}
		t.Skip("restricted identity not configured: " + p.Name)
	}
	restrictedCfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid restricted DSN for profile: " + p.Name)
	}
	if restrictedCfg.Mode != oceanbase.ModeAuto && restrictedCfg.Mode != p.Mode {
		t.Fatal("restricted identity mode differs from profile: " + p.Name)
	}
	connector, err := oceanbase.NewConnector(restrictedCfg)
	if err != nil {
		t.Fatal(err)
	}
	restricted := sql.OpenDB(connector)
	defer func() { _ = restricted.Close() }()

	admin := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	var schema string
	schemaSQL := integrationDatabaseQuery
	unreadable := "mysql"
	if p.Mode == oceanbase.ModeOracle {
		schemaSQL = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual"
		unreadable = "SYS"
	}
	if err = admin.QueryRowContext(ctx, schemaSQL).Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "read admin schema", err)
	}
	table := fmt.Sprintf("OBGR_%d", time.Now().UnixNano())
	definition := "ID INT PRIMARY KEY,AMOUNT DECIMAL(10,2),NOTE VARCHAR(12)"
	if p.Mode == oceanbase.ModeOracle {
		definition = "ID INTEGER PRIMARY KEY,AMOUNT NUMBER(10,2),NOTE VARCHAR2(12)"
	}
	if _, err = admin.ExecContext(ctx, "CREATE TABLE "+table+" ("+definition+")"); err != nil {
		integrationtest.FailOperation(t, "create restricted metadata table", err)
	}
	defer func() {
		if _, err := admin.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop restricted metadata table", err)
		}
	}()
	// Oracle has no schema-wide SELECT grant, so the owner grants the created table.
	if p.Mode == oceanbase.ModeOracle {
		user, _, _ := strings.Cut(restrictedCfg.User, "@")
		if _, err = admin.ExecContext(ctx, "GRANT SELECT ON "+table+" TO "+user); err != nil {
			integrationtest.FailOperation(t, "grant restricted metadata table", err)
		}
	}
	pinned, err := restricted.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin restricted session", err)
	}
	defer func() { _ = pinned.Close() }()

	for _, tc := range []struct {
		label  string
		filter oceanbase.MetadataFilter
		want   int
	}{
		{"table", oceanbase.MetadataFilter{Kind: oceanbase.MetadataTables, SchemaPattern: schema, NamePattern: table}, 1},
		{"columns", oceanbase.MetadataFilter{Kind: oceanbase.MetadataColumns, SchemaPattern: schema, NamePattern: table}, 3},
		{"primary-key", oceanbase.MetadataFilter{Kind: oceanbase.MetadataPrimaryKeys, SchemaPattern: schema, NamePattern: table}, 1},
		{"unreadable-schema-tables", oceanbase.MetadataFilter{Kind: oceanbase.MetadataTables, SchemaPattern: unreadable}, 0},
		{"unreadable-schema-columns", oceanbase.MetadataFilter{Kind: oceanbase.MetadataColumns, SchemaPattern: unreadable, NamePattern: "user"}, 0},
	} {
		rows, err := oceanbase.Metadata(ctx, pinned, tc.filter)
		if err != nil {
			integrationtest.FailOperation(t, "restricted metadata "+tc.label, err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			integrationtest.FailOperation(t, "restricted metadata "+tc.label+" rows", err)
		}
		if err = rows.Close(); err != nil {
			integrationtest.FailOperation(t, "close restricted metadata "+tc.label, err)
		}
		if count != tc.want {
			t.Fatalf("restricted metadata %s returned %d rows, want %d", tc.label, count, tc.want)
		}
	}
}
