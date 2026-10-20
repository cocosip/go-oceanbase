//go:build integration

package integration

import (
	sql "database/sql"
	errors "errors"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestVersionProbeSessions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.Session.SkipVersionProbe = true
			created, err := oceanbase.NewConnector(cfg)
			if err != nil {
				integrationtest.FailOperation(t, "version probe connector", err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			pinned, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "initialize without version probe", err)
			}
			defer func() { _ = pinned.Close() }()
			tx, err := pinned.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				integrationtest.FailOperation(t, "begin without version probe", err)
			}
			defer func() { _ = tx.Rollback() }()
			query := integrationtest.SelectParameterSQL
			if p.Mode == oceanbase.ModeOracle {
				query += integrationtest.OracleDualSuffix
			}
			var value int
			if err = tx.QueryRowContext(ctx, query, 42).Scan(&value); err != nil || value != 42 {
				t.Fatal("ordinary query without version probe failed")
			}
			if err = tx.Rollback(); err != nil {
				integrationtest.FailOperation(t, "rollback without version probe", err)
			}
			if p.Mode == oceanbase.ModeOracle {
				_, err = oceanbase.OpenCursor(ctx, pinned, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive}, 42)
				if !errors.Is(err, oceanbase.ErrUnsupported) {
					t.Fatal("unknown version enabled sensitive cursor")
				}
			}
			if err = pinned.PingContext(ctx); err != nil {
				integrationtest.FailOperation(t, "ping after skipped probe", err)
			}
		})
	}
}
