//go:build integration

package integration

import (
	os "os"
	testing "testing"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestCancelQuerySessions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestCancelQuerySession(t, p) })
	}
}
