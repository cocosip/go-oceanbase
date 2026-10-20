//go:build integration

package execution_test

import (
	sql "database/sql"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMultiResult(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.Mode = p.Mode
			cfg.AllowMultiStatements = p.Mode == oceanbase.ModeMySQL
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid multi-result config")
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("obgo_multi_%d", time.Now().UnixNano())
			if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,v INTEGER)"); err != nil {
				integrationtest.FailOperation(t, "create multi table", err)
			}
			defer func() {
				if _, err = db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop multi table", err)
				}
			}()
			if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(1,0)"); err != nil {
				integrationtest.FailOperation(t, "insert multi row", err)
			}
			conn, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "pin multi session", err)
			}
			defer func() { _ = conn.Close() }()
			query := "UPDATE " + table + " SET v=1; SELECT v FROM " + table + "; UPDATE " + table + " SET v=2"
			if p.Mode == oceanbase.ModeOracle {
				query = "BEGIN UPDATE " + table + " SET v=1; UPDATE " + table + " SET v=2; END;"
			}
			e, err := oceanbase.Execute(ctx, conn, query)
			if err != nil {
				integrationtest.FailOperation(t, "execute multi result", err)
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
				integrationtest.FailOperation(t, "close multi result", err)
			}
			snapshot := e.Snapshot()
			if !snapshot.Done || snapshot.Err != nil {
				t.Fatal(snapshot)
			}
			if p.Mode == oceanbase.ModeMySQL {
				if len(snapshot.Results) != 3 || snapshot.Results[0].Kind != oceanbase.ResultCommand || snapshot.Results[1].RowSetIndex != 0 || snapshot.Results[2].Kind != oceanbase.ResultCommand {
					t.Fatal(snapshot)
				}
			} else if len(snapshot.Results) != 1 {
				t.Fatal("anonymous block synthesized internal counts", snapshot)
			}
			if err = conn.Close(); err != nil {
				t.Fatal(err)
			}
			if p.Mode == oceanbase.ModeMySQL {
				integrationTestRowsAffectedPolicy(t, p, table)
			}
		})
	}
}
