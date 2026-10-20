//go:build integration

package execution_test

import (
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	os "os"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestLocalImport(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestLocalImportProfile(t, p) })
	}
}

func TestMySQLImportCommandBoundary(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.AllowMultiStatements = true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				integrationtest.FailOperation(t, "multi-command import config", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			c, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "pin multi-command import", err)
			}
			defer func() { _ = c.Close() }()
			table := fmt.Sprintf("OBGLIBOUND_%d", time.Now().UnixNano())
			if _, err = c.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(32))"+integrationIntegrationInnoDBSuffix); err != nil {
				integrationtest.FailOperation(t, "create import boundary table", err)
			}
			defer func() {
				_, _ = c.ExecContext(integrationtest.TestContext(t), "ROLLBACK")
				if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop import boundary table", err)
				}
			}()
			command := "START TRANSACTION; INSERT INTO " + table + " VALUES (9,'before-import'); LOAD DATA LOCAL INFILE 'input' INTO TABLE " + table + " FIELDS TERMINATED BY '\\t' LINES TERMINATED BY '\\n' (ID,V)"
			reader := strings.NewReader("1\tx\n2\ty\n")
			_, importErr := oceanbase.Import(ctx, c, command, &oceanbase.LocalInfile{Name: integrationLocalImportName, Reader: reader})
			if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
				integrationtest.FailOperation(t, "rollback import boundary", err)
			}
			var count int
			if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
				integrationtest.FailOperation(t, "read import boundary effects", err)
			}
			if !errors.Is(importErr, oceanbase.ErrUnsupported) || reader.Len() != len("1\tx\n2\ty\n") || count != 0 {
				t.Fatalf("unsafe multi-command import: err=%v unread=%d committed rows=%d", importErr, reader.Len(), count)
			}
		})
	}
}
