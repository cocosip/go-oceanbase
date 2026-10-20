//go:build integration

package statement_test

import (
	sql "database/sql"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestPrepareEquivalence(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			var results [2]sql.NullString
			for i, mode := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				cfg := integrationtest.ProfileConfig(t, p)
				cfg.Mode = p.Mode
				cfg.PrepareMode = mode
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal("invalid prepare config")
				}
				db := sql.OpenDB(connector)
				query := integrationtest.SelectParameterSQL
				if p.Mode == oceanbase.ModeOracle {
					query += integrationtest.OracleDualSuffix
				}
				stmt, err := db.PrepareContext(integrationtest.TestContext(t), query)
				if err != nil {
					_ = db.Close()
					integrationtest.FailOperation(t, "prepare equivalence", err)
				}
				value := "quoted'\\中文"
				err = stmt.QueryRowContext(integrationtest.TestContext(t), value).Scan(&results[i])
				_ = stmt.Close()
				_ = db.Close()
				if err != nil {
					integrationtest.FailOperation(t, "prepare value equivalence", err)
				}
				if !results[i].Valid || results[i].String != value {
					t.Fatal("prepared value changed")
				}
			}
			if results[0] != results[1] {
				t.Fatal("client and server values differ")
			}
		})
	}
}
