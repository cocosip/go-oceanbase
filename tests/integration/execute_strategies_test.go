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

func TestExecuteStrategies(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			for _, optimized := range []bool{false, true} {
				t.Run(fmt.Sprint(optimized), func(t *testing.T) { testExecuteStrategy(t, p, optimized) })
			}
		})
	}
}

func testExecuteStrategy(t *testing.T, p profile, optimized bool) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.Execution = oceanbase.ExecutionOptions{}
	if optimized {
		cfg.Execution = oceanbase.ExecutionOptions{PrepareExecute: true, StatementCacheSize: 4, ReuseParameterTypes: true, StatementChecksum: true}
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid strategy config")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() {
		if err := db.Close(); err != nil {
			t.Error("close optimized connection", err)
		}
	}()
	ctx := testContext(t)
	table := fmt.Sprintf("OBGEXEC_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"); err != nil {
		failOperation(t, "create execution table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop execution table", err)
		}
	}()
	insert := "INSERT INTO " + table + " VALUES (?,?)"
	for i, value := range []any{"中🙂", nil, "strategy-updated"} {
		res, err := db.ExecContext(ctx, insert, i, value)
		if err != nil {
			failOperation(t, "strategy insert", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatal("strategy affected rows", n, err)
		}
	}
	if _, err = db.ExecContext(ctx, insert, 0, "duplicate"); !duplicateKey(err, p.mode) {
		t.Fatal("strategy duplicate key error code mismatch")
	}
	for i, want := range []sql.NullString{{String: "中🙂", Valid: true}, {}, {String: "strategy-updated", Valid: true}} {
		var got sql.NullString
		if err = db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", i).Scan(&got); err != nil {
			failOperation(t, "strategy read", err)
		}
		if got != want {
			t.Fatal("strategy changed result", got, want)
		}
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 3 {
		t.Fatal("strategy replayed business input", count, err)
	}
	testStrategyOutput(t, p, db)
}

func testStrategyOutput(t *testing.T, p profile, db *sql.DB) {
	t.Helper()
	query := "BEGIN ? := 42; END;"
	if p.mode == oceanbase.ModeMySQL {
		procedure := fmt.Sprintf("OBGEXECP_%d", time.Now().UnixNano())
		if _, err := db.ExecContext(testContext(t), "CREATE PROCEDURE "+procedure+"(OUT y BIGINT) SET y=42"); err != nil {
			failOperation(t, "create strategy procedure", err)
		}
		defer func() {
			if _, err := db.ExecContext(testContext(t), "DROP PROCEDURE "+procedure); err != nil {
				failOperation(t, "drop strategy procedure", err)
			}
		}()
		query = "CALL " + procedure + "(?)"
	}
	for range 2 {
		var got int64
		if _, err := db.ExecContext(testContext(t), query, sql.Out{Dest: &got}); err != nil {
			failOperation(t, "strategy output", err)
		}
		if got != 42 {
			t.Fatal("strategy changed output")
		}
	}
}
