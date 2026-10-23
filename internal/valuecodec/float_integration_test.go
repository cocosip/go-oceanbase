//go:build integration

package valuecodec_test

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestOracleBinaryDoubleRange(t *testing.T) {
	profile := integrationtest.Profile{Name: "oracle", Env: "OB_ORACLE_DSN", Mode: oceanbase.ModeOracle}
	for _, combined := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("combined=%t/prepared=%t", combined, explicit), func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.PrepareMode = oceanbase.PrepareServer
				cfg.Execution.PrepareExecute, cfg.Execution.ReuseParameterTypes = combined, true
				cfg.Execution.StatementCacheSize = 4
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				ctx := integrationtest.TestContext(t)
				table := fmt.Sprintf("OBGD_%d", time.Now().UnixNano())
				if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER PRIMARY KEY,V BINARY_DOUBLE)"); err != nil {
					integrationtest.FailOperation(t, "create binary double table", err)
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
						t.Error(err)
					}
				}()
				query := "INSERT INTO " + table + " (ID,V) VALUES (?,?)"
				exec := func(id int, value any) (sql.Result, error) { return db.ExecContext(ctx, query, id, value) }
				if explicit {
					stmt, err := db.PrepareContext(ctx, query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = stmt.Close() }()
					exec = func(id int, value any) (sql.Result, error) { return stmt.ExecContext(ctx, id, value) }
				}
				maxFloat := math.MaxFloat64
				for i, input := range []any{maxFloat, -maxFloat, 1e200, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, &maxFloat, (*float64)(nil), sql.NullFloat64{}, sql.NullFloat64{Float64: -maxFloat, Valid: true}, maxFloat} {
					if _, err := exec(i, input); err != nil {
						integrationtest.FailOperation(t, "insert binary double", err)
					}
					var want sql.NullFloat64
					switch v := input.(type) {
					case float64:
						want = sql.NullFloat64{Float64: v, Valid: true}
					case *float64:
						if v != nil {
							want = sql.NullFloat64{Float64: *v, Valid: true}
						}
					case sql.NullFloat64:
						want = v
					}
					var got sql.NullFloat64
					if err := db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", i).Scan(&got); err != nil {
						integrationtest.FailOperation(t, "read binary double", err)
					}
					if got.Valid != want.Valid || got.Valid && math.Float64bits(got.Float64) != math.Float64bits(want.Float64) {
						t.Fatalf("row=%d got=%+v want=%+v", i, got, want)
					}
				}
			})
		}
	}
}
