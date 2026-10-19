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

const localImportName = "input"

func TestMySQLImportCommandBoundary(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.AllowMultiStatements = true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "multi-command import config", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := testContext(t)
			c, err := db.Conn(ctx)
			if err != nil {
				failOperation(t, "pin multi-command import", err)
			}
			defer func() { _ = c.Close() }()
			table := fmt.Sprintf("OBGLIBOUND_%d", time.Now().UnixNano())
			if _, err = c.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(32))"+integrationInnoDBSuffix); err != nil {
				failOperation(t, "create import boundary table", err)
			}
			defer func() {
				_, _ = c.ExecContext(testContext(t), "ROLLBACK")
				if _, err := c.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop import boundary table", err)
				}
			}()
			command := "START TRANSACTION; INSERT INTO " + table + " VALUES (9,'before-import'); LOAD DATA LOCAL INFILE 'input' INTO TABLE " + table + " FIELDS TERMINATED BY '\\t' LINES TERMINATED BY '\\n' (ID,V)"
			reader := strings.NewReader("1\tx\n2\ty\n")
			_, importErr := oceanbase.Import(ctx, c, command, &oceanbase.LocalInfile{Name: localImportName, Reader: reader})
			if _, err = c.ExecContext(ctx, "ROLLBACK"); err != nil {
				failOperation(t, "rollback import boundary", err)
			}
			var count int
			if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
				failOperation(t, "read import boundary effects", err)
			}
			if !errors.Is(importErr, oceanbase.ErrUnsupported) || reader.Len() != len("1\tx\n2\ty\n") || count != 0 {
				t.Fatalf("unsafe multi-command import: err=%v unread=%d committed rows=%d", importErr, reader.Len(), count)
			}
		})
	}
}

func TestLocalImport(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testLocalImportProfile(t, p) })
	}
}

func testLocalImportProfile(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	table := fmt.Sprintf("OBGLI_%d", time.Now().UnixNano())
	command := os.Getenv(strings.TrimSuffix(p.env, "_DSN") + "_LOCAL_IMPORT_SQL")
	if command == "" {
		if p.mode == oceanbase.ModeOracle {
			if os.Getenv("OB_TEST_REQUIRE") == "1" {
				t.Fatal("Oracle profile requires LOCAL_IMPORT_SQL with {table} and request name input")
			}
			t.Skip("Oracle LOCAL import SQL/service support not configured")
		}
		command = "LOAD DATA LOCAL INFILE 'input' INTO TABLE {table} FIELDS TERMINATED BY '\\t' LINES TERMINATED BY '\\n' (ID,V)"
	}
	if !strings.Contains(command, "{table}") {
		t.Fatal("LOCAL_IMPORT_SQL must contain {table}")
	}
	command = strings.ReplaceAll(command, "{table}", table)
	suffix := ""
	if p.mode == oceanbase.ModeMySQL {
		suffix = integrationInnoDBSuffix
	}
	if _, err := db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(32))"+suffix); err != nil {
		failOperation(t, "create local import table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop local import table", err)
		}
	}()
	if p.mode == oceanbase.ModeMySQL {
		testMySQLImportAutocommit(t, db, table, command)
		return
	}
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(testContext(t), nil)
		if err != nil {
			failOperation(t, "begin local import transaction", err)
		}
		func() {
			defer func() { _ = tx.Rollback() }()
			result, err := oceanbase.Import(testContext(t), tx, command, &oceanbase.LocalInfile{Name: localImportName, Reader: strings.NewReader("1\tx\n2\ty\n")})
			if err != nil {
				failOperation(t, "local import", err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 2 {
				t.Fatal("local import affected count differs")
			}
			var value string
			if err := tx.QueryRowContext(testContext(t), "SELECT V FROM "+table+" WHERE ID=2").Scan(&value); err != nil || value != "y" {
				t.Fatal("local import content differs")
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				failOperation(t, "finish local import transaction", err)
			}
		}()
		want := 0
		if commit {
			want = 2
		}
		var n int
		if err := db.QueryRowContext(testContext(t), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Fatal("local import commit/rollback differs")
		}
	}
	tx, err := db.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin failed local import transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := oceanbase.Import(testContext(t), tx, command, &oceanbase.LocalInfile{Name: localImportName, Reader: strings.NewReader(strings.Repeat("3\tz\n", 20000)), MaxBytes: 65536}); !errors.Is(err, oceanbase.ErrLocalInfile) {
		t.Fatal("local import size failure was not reported")
	}
	if _, err := tx.ExecContext(testContext(t), "SELECT 1"); err == nil {
		t.Fatal("failed import left transaction session reusable")
	}
	_ = tx.Rollback()
	// The failed session is discarded; a new connection must remain usable.
	if err := db.PingContext(testContext(t)); err != nil {
		failOperation(t, "ping after failed local import", err)
	}
	var count int
	if err := db.QueryRowContext(testContext(t), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed import retained partial transaction effects")
	}
}

func testMySQLImportAutocommit(t *testing.T, db *sql.DB, table, command string) {
	t.Helper()
	ctx := testContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		failOperation(t, "begin protected import transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (9,'before-import')"); err != nil {
		failOperation(t, "write before refused import", err)
	}
	data := "1\tx\n2\ty\n"
	reader := strings.NewReader(data)
	if _, err := oceanbase.Import(ctx, tx, command, &oceanbase.LocalInfile{Name: localImportName, Reader: reader}); !errors.Is(err, oceanbase.ErrUnsupported) || reader.Len() != len(data) {
		t.Fatal("transactional import not refused before reading input")
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatal("refused import changed transaction contents", err)
	}
	if err = tx.Rollback(); err != nil {
		failOperation(t, "rollback protected import transaction", err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatal("refused import committed preceding DML", err)
	}
	result, err := oceanbase.Import(ctx, db, command, &oceanbase.LocalInfile{Name: localImportName, Reader: strings.NewReader(data)})
	if err != nil {
		failOperation(t, "autocommit import", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 2 {
		t.Fatal("autocommit import affected count differs")
	}
	rows, err := db.QueryContext(ctx, "SELECT ID,V FROM "+table+" ORDER BY ID")
	if err != nil {
		failOperation(t, "read autocommit import", err)
	}
	defer func() { _ = rows.Close() }()
	for i, want := range []string{"x", "y"} {
		if !rows.Next() {
			t.Fatal("missing imported row", rows.Err())
		}
		var id int
		var value string
		if err := rows.Scan(&id, &value); err != nil || id != i+1 || value != want {
			t.Fatal("imported content mismatch", err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected imported rows", rows.Err())
	}
	if err = rows.Close(); err != nil {
		failOperation(t, "close imported rows", err)
	}
	if err = db.PingContext(ctx); err != nil {
		failOperation(t, "ping after autocommit import", err)
	}
}
