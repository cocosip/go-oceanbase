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

func integrationTestRowsAffectedPolicy(t *testing.T, p integrationtest.Profile, table string) {
	t.Helper()
	for _, mode := range []oceanbase.RowsAffectedMode{oceanbase.MatchedRows, oceanbase.ChangedRows} {
		cfg := integrationtest.ProfileConfig(t, p)
		cfg.RowsAffectedMode = mode
		connector, err := oceanbase.NewConnector(cfg)
		if err != nil {
			t.Fatal("invalid count config")
		}
		db := sql.OpenDB(connector)
		defer func() { _ = db.Close() }()
		result, err := db.ExecContext(integrationtest.TestContext(t), "UPDATE "+table+" SET v=2 WHERE id=1")
		if err != nil {
			integrationtest.FailOperation(t, "no-op update", err)
		}
		count, err := result.RowsAffected()
		want := int64(1)
		if mode == oceanbase.ChangedRows {
			want = 0
		}
		if err != nil || count != want {
			t.Fatal(count, want, err)
		}
		for _, changed := range []bool{false, true} {
			value := 2
			if changed {
				value = 3
			}
			result, err = db.ExecContext(integrationtest.TestContext(t), fmt.Sprintf("INSERT INTO %s (id,v) VALUES(1,%d) ON DUPLICATE KEY UPDATE v=%d", table, value, value))
			if err != nil {
				integrationtest.FailOperation(t, "upsert count", err)
			}
			count, err = result.RowsAffected()
			expected := want
			if changed {
				expected = 2
			}
			if err != nil || count != expected {
				t.Fatal("upsert count", count, expected, err)
			}
		}
		_, err = db.ExecContext(integrationtest.TestContext(t), "UPDATE "+table+" SET v=2 WHERE id=1")
		if err != nil {
			integrationtest.FailOperation(t, "restore count fixture", err)
		}
	}
}

func integrationTestLocalImportProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	table := fmt.Sprintf("OBGLI_%d", time.Now().UnixNano())
	command := os.Getenv(strings.TrimSuffix(p.Env, "_DSN") + "_LOCAL_IMPORT_SQL")
	if command == "" {
		command = "LOAD DATA LOCAL INFILE 'input' INTO TABLE {table} FIELDS TERMINATED BY '\\t' LINES TERMINATED BY '\\n' (ID,V)"
	}
	if !strings.Contains(command, "{table}") {
		t.Fatal("LOCAL_IMPORT_SQL must contain {table}")
	}
	command = strings.ReplaceAll(command, "{table}", table)
	suffix := ""
	if p.Mode == oceanbase.ModeMySQL {
		suffix = integrationIntegrationInnoDBSuffix
	}
	if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(32))"+suffix); err != nil {
		integrationtest.FailOperation(t, "create local import table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop local import table", err)
		}
	}()
	integrationTestImportAutocommit(t, db, table, command)
}

func integrationTestImportAutocommit(t *testing.T, db *sql.DB, table, command string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin protected import transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (9,'before-import')"); err != nil {
		integrationtest.FailOperation(t, "write before refused import", err)
	}
	data := "1\tx\n2\ty\n"
	reader := strings.NewReader(data)
	if _, err := oceanbase.Import(ctx, tx, command, &oceanbase.LocalInfile{Name: integrationLocalImportName, Reader: reader}); !errors.Is(err, oceanbase.ErrUnsupported) || reader.Len() != len(data) {
		t.Fatal("transactional import not refused before reading input")
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatal("refused import changed transaction contents", err)
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback protected import transaction", err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatal("refused import committed preceding DML", err)
	}
	result, err := oceanbase.Import(ctx, db, command, &oceanbase.LocalInfile{Name: integrationLocalImportName, Reader: strings.NewReader(data)})
	if err != nil {
		integrationtest.FailOperation(t, "autocommit import", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 2 {
		t.Fatal("autocommit import affected count differs")
	}
	rows, err := db.QueryContext(ctx, "SELECT ID,V FROM "+table+" ORDER BY ID")
	if err != nil {
		integrationtest.FailOperation(t, "read autocommit import", err)
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
		integrationtest.FailOperation(t, "close imported rows", err)
	}
	if err = db.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "ping after autocommit import", err)
	}
}

const integrationLocalImportName = "input"

const integrationIntegrationInnoDBSuffix = " ENGINE=InnoDB"
