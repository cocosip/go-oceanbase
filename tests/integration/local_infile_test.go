//go:build integration

package integration

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

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
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(testContext(t), nil)
		if err != nil {
			failOperation(t, "begin local import transaction", err)
		}
		func() {
			defer func() { _ = tx.Rollback() }()
			result, err := oceanbase.Import(testContext(t), tx, command, &oceanbase.LocalInfile{Name: "input", Reader: strings.NewReader("1\tx\n2\ty\n")})
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
	if _, err := oceanbase.Import(testContext(t), tx, command, &oceanbase.LocalInfile{Name: "input", Reader: strings.NewReader(strings.Repeat("3\tz\n", 20000)), MaxBytes: 65536}); !errors.Is(err, oceanbase.ErrLocalInfile) {
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
