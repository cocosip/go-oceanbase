package config

import (
	"testing"
)

func TestLegacyExecutionOptionsRejected(t *testing.T) {
	for _, option := range []string{"usePipelineAuth", "enableQueryTimeouts", "encloseParamInParentheses", "serverAffinityOrder"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=true"); err == nil {
			t.Fatal("Java option silently accepted instead of its Go API", option)
		}
	}
}
