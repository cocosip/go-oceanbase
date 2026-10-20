//go:build integration

package integration

import (
	sql "database/sql"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestLocalTx(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, p)
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("obgo_tx_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
				integrationtest.FailOperation(t, "create tx table", err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop tx table", err)
				}
			})
			tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				integrationtest.FailOperation(t, "begin tx", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 1); err != nil {
				integrationtest.FailOperation(t, "tx insert", err)
			}
			if err = oceanbase.Savepoint(ctx, tx, "before_second"); err != nil {
				integrationtest.FailOperation(t, "savepoint", err)
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 2); err != nil {
				integrationtest.FailOperation(t, "tx insert second", err)
			}
			if err = oceanbase.RollbackTo(ctx, tx, "before_second"); err != nil {
				integrationtest.FailOperation(t, "rollback to", err)
			}
			var n int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if err = tx.Commit(); err != nil {
				integrationtest.FailOperation(t, "commit", err)
			}
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			readonly, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				integrationtest.FailOperation(t, "begin readonly", err)
			}
			if _, err = readonly.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 3); err == nil {
				t.Fatal("readonly transaction allowed write")
			}
			if err = readonly.Rollback(); err != nil {
				integrationtest.FailOperation(t, "readonly rollback", err)
			}
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}
