package control_test

import (
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/control"
)

func TestSavepointTxDeadline(t *testing.T) {
	for _, mode := range []config.CompatibilityMode{config.CompatibilityModeMySQL, config.CompatibilityModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			testSavepointTxContext(t, mode, testStandard, control.CreateSavepoint, true)
		})
	}
}

func TestSavepointTxCancellation(t *testing.T) {
	for _, mode := range []config.CompatibilityMode{config.CompatibilityModeMySQL, config.CompatibilityModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			operations := []control.SavepointKind{control.CreateSavepoint, control.RollbackSavepoint}
			if mode == config.CompatibilityModeMySQL {
				operations = append(operations, control.ReleaseSavepoint)
			}
			for _, operation := range operations {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, framing, operation), func(t *testing.T) {
					testSavepointTxContext(t, mode, framing, operation, false)
				})
			}
		}
	}
}
