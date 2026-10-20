//go:build integration

package valuecodec_test

import (
	os "os"
	testing "testing"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestAllScalarTypes(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			for _, path := range []string{integrationScalarTextPath, "server", "client"} {
				t.Run(path, func(t *testing.T) { integrationTestScalarProfile(t, p, path) })
			}
		})
	}
}
