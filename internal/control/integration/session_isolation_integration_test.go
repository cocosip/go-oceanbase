//go:build integration

package integration

import (
	context "context"
	sql "database/sql"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestBorrowerSessionIsolation verifies that two pool borrowers do not share
// uncommitted effects, row locks or session variables.
func TestBorrowerSessionIsolation(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestBorrowerSessionIsolation(t, p) })
	}
}

func integrationTestBorrowerSessionIsolation(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	connector, err := oceanbase.NewConnector(integrationtest.ProfileConfig(t, p))
	if err != nil {
		integrationtest.FailOperation(t, "configure isolation pool", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGSI_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,v VARCHAR(20))"); err != nil {
		integrationtest.FailOperation(t, "create isolation table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop isolation table", err)
		}
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,'one')"); err != nil {
		integrationtest.FailOperation(t, "seed isolation table", err)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin isolation holder", err)
	}
	defer func() { _ = holder.Close() }()
	count := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			integrationtest.FailOperation(t, "count isolation rows", err)
		}
		return n
	}
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin isolation transaction", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (2,'two')"); err != nil {
		integrationtest.FailOperation(t, "insert isolation transaction", err)
	}
	if n := count(); n != 1 {
		t.Fatal("uncommitted borrower row visible", n)
	}
	if err = tx.Commit(); err != nil {
		integrationtest.FailOperation(t, "commit isolation transaction", err)
	}
	if n := count(); n != 2 {
		t.Fatal("committed borrower row missing", n)
	}
	tx, err = holder.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin isolation rollback", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (3,'three')"); err != nil {
		integrationtest.FailOperation(t, "insert isolation rollback", err)
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback isolation transaction", err)
	}
	if n := count(); n != 2 {
		t.Fatal("rolled-back borrower row visible", n)
	}
	tx, err = holder.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin isolation lock", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET v='locked' WHERE id=1"); err != nil {
		integrationtest.FailOperation(t, "lock isolation row", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err = db.ExecContext(waitCtx, "UPDATE "+table+" SET v='blocked' WHERE id=1")
	cancel()
	if err == nil {
		t.Fatal("second borrower updated a locked row")
	}
	if err = tx.Commit(); err != nil {
		integrationtest.FailOperation(t, "commit isolation lock", err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE "+table+" SET v='after' WHERE id=1"); err != nil {
		integrationtest.FailOperation(t, "update after lock release", err)
	}
	if p.Mode == oceanbase.ModeMySQL {
		if _, err = holder.ExecContext(ctx, "SET @obgo_isolation = 7"); err != nil {
			integrationtest.FailOperation(t, "set isolation variable", err)
		}
		var value sql.NullInt64
		if err = db.QueryRowContext(ctx, "SELECT @obgo_isolation").Scan(&value); err != nil {
			integrationtest.FailOperation(t, "read isolation variable", err)
		}
		if value.Valid {
			t.Fatal("session variable leaked between borrowers")
		}
	}
	if err = holder.Close(); err != nil {
		integrationtest.FailOperation(t, "close isolation holder", err)
	}
	if stats := db.Stats(); stats.InUse != 0 {
		t.Fatal("isolation workload retained a lease", stats)
	}
}
