//go:build integration

package integration

import (
	errors "errors"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestSessionCollation(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			if p.Mode == oceanbase.ModeOracle {
				cfg.Session.Collation = "utf8mb4_bin"
				_, err := oceanbase.NewConnector(cfg)
				var configErr *oceanbase.ConfigError
				if !errors.As(err, &configErr) || configErr.Field != "Session.Collation" {
					t.Fatal("Oracle collation not explicitly rejected")
				}
				return
			}
			for _, collation := range []string{"utf8mb4_bin", "utf8mb4_general_ci"} {
				t.Run(collation, func(t *testing.T) { integrationTestSessionCollation(t, cfg, collation) })
			}
		})
	}
}
