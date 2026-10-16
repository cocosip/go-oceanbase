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

func TestMultiResult(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.Mode = p.mode
			cfg.AllowMultiStatements = p.mode == oceanbase.ModeMySQL
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid multi-result config")
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := testContext(t)
			table := fmt.Sprintf("obgo_multi_%d", time.Now().UnixNano())
			if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,v INTEGER)"); err != nil {
				failOperation(t, "create multi table", err)
			}
			defer func() {
				if _, err = db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop multi table", err)
				}
			}()
			if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(1,0)"); err != nil {
				failOperation(t, "insert multi row", err)
			}
			conn, err := db.Conn(ctx)
			if err != nil {
				failOperation(t, "pin multi session", err)
			}
			defer func() { _ = conn.Close() }()
			query := "UPDATE " + table + " SET v=1; SELECT v FROM " + table + "; UPDATE " + table + " SET v=2"
			if p.mode == oceanbase.ModeOracle {
				query = "BEGIN UPDATE " + table + " SET v=1; UPDATE " + table + " SET v=2; END;"
			}
			e, err := oceanbase.Execute(ctx, conn, query)
			if err != nil {
				failOperation(t, "execute multi result", err)
			}
			for rows := e.Rows(); ; {
				for rows.Next() {
					var v int
					if err = rows.Scan(&v); err != nil || v != 1 {
						t.Fatal(v, err)
					}
				}
				if !rows.NextResultSet() {
					break
				}
			}
			if err = e.Close(); err != nil {
				failOperation(t, "close multi result", err)
			}
			snapshot := e.Snapshot()
			if !snapshot.Done || snapshot.Err != nil {
				t.Fatal(snapshot)
			}
			if p.mode == oceanbase.ModeMySQL {
				if len(snapshot.Results) != 3 || snapshot.Results[0].Kind != oceanbase.ResultCommand || snapshot.Results[1].RowSetIndex != 0 || snapshot.Results[2].Kind != oceanbase.ResultCommand {
					t.Fatal(snapshot)
				}
			} else if len(snapshot.Results) != 1 {
				t.Fatal("anonymous block synthesized internal counts", snapshot)
			}
			if err = conn.Close(); err != nil {
				t.Fatal(err)
			}
			if p.mode == oceanbase.ModeMySQL {
				testRowsAffectedPolicy(t, p, table)
			}
		})
	}
}

func testRowsAffectedPolicy(t *testing.T, p profile, table string) {
	t.Helper()
	for _, mode := range []oceanbase.RowsAffectedMode{oceanbase.MatchedRows, oceanbase.ChangedRows} {
		cfg := profileConfig(t, p)
		cfg.RowsAffectedMode = mode
		connector, err := oceanbase.NewConnector(cfg)
		if err != nil {
			t.Fatal("invalid count config")
		}
		db := sql.OpenDB(connector)
		defer func() { _ = db.Close() }()
		result, err := db.ExecContext(testContext(t), "UPDATE "+table+" SET v=2 WHERE id=1")
		if err != nil {
			failOperation(t, "no-op update", err)
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
			result, err = db.ExecContext(testContext(t), fmt.Sprintf("INSERT INTO %s (id,v) VALUES(1,%d) ON DUPLICATE KEY UPDATE v=%d", table, value, value))
			if err != nil {
				failOperation(t, "upsert count", err)
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
		_, err = db.ExecContext(testContext(t), "UPDATE "+table+" SET v=2 WHERE id=1")
		if err != nil {
			failOperation(t, "restore count fixture", err)
		}
	}
}
