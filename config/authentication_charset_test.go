package config

import (
	"errors"
	"strings"
	"testing"
)

func TestAuthPluginOptionsDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://user@localhost/?allowOldPassword=true&passwordCharset=latin1")
	if err != nil || !cfg.Auth.AllowOldPassword || !strings.EqualFold(cfg.Auth.PasswordCharset, authPasswordCharsetTest) {
		t.Fatalf("auth DSN: %+v %v", cfg.Auth, err)
	}
	for _, value := range []string{"1", "TRUE", "false&allowOldPassword=true"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?allowOldPassword=" + value); err == nil {
			t.Fatal("invalid bool accepted")
		}
	}
	_, err = ParseDSN("oceanbase://user@localhost/?passwordCharset=unknown")
	var config *ConfigError
	if !errors.As(err, &config) {
		t.Fatal("invalid charset", err)
	}
}
