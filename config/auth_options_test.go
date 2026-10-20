package config

import (
	"strings"
	"testing"
)

func TestAuthJavaDSNRejected(t *testing.T) {
	for _, option := range []string{"credentialType", "userKey", "pwdKey", "serverRsaPublicKeyFile", "allowPublicKeyRetrieval"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=auth-input-secret"); err == nil || strings.Contains(err.Error(), "auth-input-secret") {
			t.Fatal("unsupported Java option accepted or disclosed", option, err)
		}
	}
}
