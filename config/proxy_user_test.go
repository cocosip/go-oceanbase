package config

import (
	"errors"
	"strings"
	"testing"
)

func TestProxyUserConfig(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@localhost/?compatibilityMode=oracle&proxyUser=client_user")
	if err != nil || cfg.ProxyUser != testProxyUser {
		t.Fatal("explicit proxy identity not parsed", err)
	}
	for _, tc := range []struct {
		value, charset string
		mode           CompatibilityMode
	}{
		{"proxy-private\x00", "", CompatibilityModeOracle},
		{"proxy-private\xff", "", CompatibilityModeOracle},
		{"private测试", testCharsetASCII, CompatibilityModeOracle},
		{testProxyUser, "", CompatibilityModeMySQL},
	} {
		_, err := Normalize(Config{User: testUser, ProxyUser: tc.value, CompatibilityMode: tc.mode, Session: SessionOptions{Charset: tc.charset}})
		var invalid *ConfigError
		if !errors.As(err, &invalid) || invalid.Field != "ProxyUser" || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe proxy identity accepted or exposed", err)
		}
	}
	for _, option := range []string{"proxyUser=", "proxyUser=a&proxyUser=b"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option); err == nil {
			t.Fatal("invalid proxyUser option accepted")
		}
	}
}
