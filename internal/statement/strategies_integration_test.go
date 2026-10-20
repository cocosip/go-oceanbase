//go:build integration

package statement_test

import (
	fmt "fmt"
	os "os"
	testing "testing"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestExecuteStrategies(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			for _, optimized := range []bool{false, true} {
				t.Run(fmt.Sprint(optimized), func(t *testing.T) { integrationTestExecuteStrategy(t, p, optimized) })
			}
		})
	}
}
