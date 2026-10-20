package config

import (
	"testing"
)

func TestExecutionColumnJavaDSNRejected(t *testing.T) {
	for _, option := range []string{"useOldAliasMetadataBehavior", "blankTableNameMeta", "useCompatibleMetadata"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=true"); err == nil {
			t.Fatal("Java metadata option accepted", option)
		}
	}
}
