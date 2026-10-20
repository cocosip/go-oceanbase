//go:build integration

package catalog_test

import (
	fmt "fmt"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMySQLMetadataRegressions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			t.Run("temporal-scale", func(t *testing.T) { integrationTestMySQLTemporalMetadata(t, p) })
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprint(prepare), func(t *testing.T) { integrationTestMySQLNamedCall(t, p, prepare) })
			}
		})
	}
}
