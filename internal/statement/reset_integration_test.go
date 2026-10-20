//go:build integration

package statement_test

import (
	sql "database/sql"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestStmtAfterReset(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.Mode = p.Mode
			cfg.ResetMode = oceanbase.ResetFull
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid reset config")
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			query := integrationtest.SelectParameterSQL
			if p.Mode == oceanbase.ModeOracle {
				query += integrationtest.OracleDualSuffix
			}
			stmt, err := db.PrepareContext(integrationtest.TestContext(t), query)
			if err != nil {
				integrationtest.FailOperation(t, "prepare before full reset", err)
			}
			defer func() {
				if err := stmt.Close(); err != nil {
					t.Error(err)
				}
			}()
			for _, want := range []int{1, 2, 3} {
				var value int
				if err = stmt.QueryRowContext(integrationtest.TestContext(t), want).Scan(&value); err != nil || value != want {
					t.Fatal(value, err)
				}
			}
		})
	}
}
