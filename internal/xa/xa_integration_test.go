//go:build integration

package xa_test

import (
	os "os"
	testing "testing"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestXARecoveryAfterDisconnect(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestXABranch(t, p, "two-phase", true) })
	}
}

func TestXATransactions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			for _, scenario := range []string{integrationXaOnePhase, "two-phase", "rollback", integrationXaSuspend, integrationXaJoin} {
				t.Run(scenario, func(t *testing.T) { integrationTestXABranch(t, p, scenario, false) })
			}
		})
	}
}
