//go:build integration

package lob_test

import (
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestNativeLOB(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			t.Run("BLOB-CLOB", func(t *testing.T) { integrationTestNativeLOBProfile(t, p) })
			if p.Mode == oceanbase.ModeOracle {
				t.Run("NCLOB", func(t *testing.T) { integrationTestNativeNCLOB(t, p) })
			}
		})
	}
}
