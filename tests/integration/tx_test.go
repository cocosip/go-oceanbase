//go:build integration

package integration

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestLocalTx(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			ctx := testContext(t)
			table := fmt.Sprintf("obgo_tx_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
				failOperation(t, "create tx table", err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop tx table", err)
				}
			})
			tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				failOperation(t, "begin tx", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 1); err != nil {
				failOperation(t, "tx insert", err)
			}
			if err = oceanbase.Savepoint(ctx, tx, "before_second"); err != nil {
				failOperation(t, "savepoint", err)
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 2); err != nil {
				failOperation(t, "tx insert second", err)
			}
			if err = oceanbase.RollbackTo(ctx, tx, "before_second"); err != nil {
				failOperation(t, "rollback to", err)
			}
			var n int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if err = tx.Commit(); err != nil {
				failOperation(t, "commit", err)
			}
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			readonly, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				failOperation(t, "begin readonly", err)
			}
			if _, err = readonly.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 3); err == nil {
				t.Fatal("readonly transaction allowed write")
			}
			if err = readonly.Rollback(); err != nil {
				failOperation(t, "readonly rollback", err)
			}
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestStmtAfterReset(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.Mode = p.mode
			cfg.ResetMode = oceanbase.ResetFull
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid reset config")
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			query := "SELECT ?"
			if p.mode == oceanbase.ModeOracle {
				query += oracleDualSuffix
			}
			stmt, err := db.PrepareContext(testContext(t), query)
			if err != nil {
				failOperation(t, "prepare before full reset", err)
			}
			defer func() {
				if err := stmt.Close(); err != nil {
					t.Error(err)
				}
			}()
			for _, want := range []int{1, 2, 3} {
				var value int
				if err = stmt.QueryRowContext(testContext(t), want).Scan(&value); err != nil || value != want {
					t.Fatal(value, err)
				}
			}
		})
	}
}
