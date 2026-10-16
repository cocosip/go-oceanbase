//go:build integration

package integration

import (
	"os"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestSmoke(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	require := os.Getenv("OB_TEST_REQUIRE")
	if require != "" && require != "0" && require != "1" {
		t.Fatal("OB_TEST_REQUIRE must be 0 or 1")
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			if err := db.PingContext(testContext(t)); err != nil {
				failOperation(t, "Ping", err)
			}
			query := "SELECT ob_version()"
			if p.mode == oceanbase.ModeOracle {
				query += oracleDualSuffix
			}
			var version string
			if err := db.QueryRowContext(testContext(t), query).Scan(&version); err != nil {
				failOperation(t, "version", err)
			}
			if version == "" {
				t.Fatal("empty OceanBase version")
			}
			t.Logf("authenticated connection, initialization, Ping and version query passed; OceanBase=%s", version)
		})
	}
}
