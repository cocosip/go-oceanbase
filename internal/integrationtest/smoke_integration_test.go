//go:build integration

package integrationtest_test

import (
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestSmoke(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	require := os.Getenv("OB_TEST_REQUIRE")
	if require != "" && require != "0" && require != "1" {
		t.Fatal("OB_TEST_REQUIRE must be 0 or 1")
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, p)
			if err := db.PingContext(integrationtest.TestContext(t)); err != nil {
				integrationtest.FailOperation(t, "Ping", err)
			}
			query := "SELECT ob_version()"
			if p.Mode == oceanbase.ModeOracle {
				query += integrationtest.OracleDualSuffix
			}
			var version string
			if err := db.QueryRowContext(integrationtest.TestContext(t), query).Scan(&version); err != nil {
				integrationtest.FailOperation(t, "version", err)
			}
			if version == "" {
				t.Fatal("empty OceanBase version")
			}
			t.Logf("authenticated connection, initialization, Ping and version query passed; OceanBase=%s", version)
		})
	}
}
