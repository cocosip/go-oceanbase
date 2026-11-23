package control_test

import (
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
)

func TestSessionReadResponseBoundary(t *testing.T) {
	for _, mode := range []config.CompatibilityMode{config.CompatibilityModeMySQL, config.CompatibilityModeOracle} {
		for _, framing := range []string{"standard", testCompression, testOB20} {
			for _, path := range []string{"error-before-row", sessionReadTailError, "no-row", sessionReadExtraRow} {
				t.Run(fmt.Sprintf("%v/%s/%s", mode, framing, path), func(t *testing.T) {
					for _, helper := range []string{sessionReadSchema, sessionReadCatalog, sessionReadInfo, sessionReadVariable} {
						if helper == sessionReadCatalog && mode == config.CompatibilityModeOracle {
							continue
						}
						t.Run(helper, func(t *testing.T) { testSessionReadResponse(t, mode, framing, path, helper) })
					}
				})
			}
		}
	}
}
