//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestQueryCRUD(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			ctx := testContext(t)
			table := fmt.Sprintf("obgo_%d", time.Now().UnixNano())
			if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, v VARCHAR(100))"); err != nil {
				failOperation(t, "create table", err)
			}
			t.Cleanup(func() {
				if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop table", err)
				}
			})
			for _, query := range []string{"INSERT INTO " + table + " VALUES (1,'hello')", "UPDATE " + table + " SET v='world' WHERE id=1"} {
				r, err := db.ExecContext(ctx, query)
				if err != nil {
					failOperation(t, "write", err)
				}
				if n, err := r.RowsAffected(); err != nil || n != 1 {
					t.Fatalf("RowsAffected=%d: %v", n, err)
				}
			}
			var value string
			if err := db.QueryRowContext(ctx, "SELECT v FROM "+table+" WHERE id=1").Scan(&value); err != nil {
				failOperation(t, "select", err)
			}
			if value != "world" {
				t.Fatal(value)
			}
			r, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=1")
			if err != nil {
				failOperation(t, "delete", err)
			}
			if n, err := r.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("delete RowsAffected=%d: %v", n, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT v FROM "+table+" WHERE id=1").Scan(&value); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("deleted row: %v", err)
			}
		})
	}
}

func TestQueryResults(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			ctx := testContext(t)
			query := "SELECT 42 AS v, NULL AS n"
			if p.mode == oceanbase.ModeOracle {
				query += " FROM dual"
			}
			rows, err := db.QueryContext(ctx, query)
			if err != nil {
				failOperation(t, "query", err)
			}
			cols, err := rows.ColumnTypes()
			if err != nil || len(cols) != 2 {
				t.Fatal(cols, err)
			}
			var n int
			var nullable sql.NullString
			if !rows.Next() {
				t.Fatal("missing row", rows.Err())
			}
			if err = rows.Scan(&n, &nullable); err != nil || n != 42 || nullable.Valid {
				t.Fatal(n, nullable, err)
			}
			if err = rows.Close(); err != nil {
				failOperation(t, "early Close", err)
			}
			if err = db.PingContext(ctx); err != nil {
				failOperation(t, "Ping after rows Close", err)
			}
			_, err = db.ExecContext(ctx, "SELECT __obgo_unknown_column FROM __obgo_unknown_table")
			var server *oceanbase.Error
			if !errors.As(err, &server) || server.Code == 0 || server.SQLState == "" {
				t.Fatalf("SQL error: %T", err)
			}
			if err = db.PingContext(ctx); err != nil {
				failOperation(t, "Ping after SQL error", err)
			}
		})
	}
}
