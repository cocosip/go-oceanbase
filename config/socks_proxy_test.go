package config

import (
	"testing"
)

func TestSOCKSJavaDSNRejected(t *testing.T) {
	for _, option := range []string{"socksProxyHost", "socksProxyPort"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=1080"); err == nil {
			t.Fatal("Java proxy option accepted", option)
		}
	}
}
