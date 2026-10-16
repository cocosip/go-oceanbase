//go:build integration

package integration

import (
	"database/sql"
	"os"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestPrepareEquivalence(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			var results [2]sql.NullString
			for i, mode := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				cfg := profileConfig(t, p)
				cfg.Mode = p.mode
				cfg.PrepareMode = mode
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal("invalid prepare config")
				}
				db := sql.OpenDB(connector)
				query := selectParameterSQL
				if p.mode == oceanbase.ModeOracle {
					query += oracleDualSuffix
				}
				stmt, err := db.PrepareContext(testContext(t), query)
				if err != nil {
					_ = db.Close()
					failOperation(t, "prepare equivalence", err)
				}
				value := "quoted'\\中文"
				err = stmt.QueryRowContext(testContext(t), value).Scan(&results[i])
				_ = stmt.Close()
				_ = db.Close()
				if err != nil {
					failOperation(t, "prepare value equivalence", err)
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
