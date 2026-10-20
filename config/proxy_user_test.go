package config

import (
	"errors"
	"strings"
	"testing"
)

func TestProxyUserConfig(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@localhost/?mode=oracle&proxyUser=client_user")
	if err != nil || cfg.ProxyUser != testProxyUser {
		t.Fatal("explicit proxy identity not parsed", err)
	}
	for _, tc := range []struct {
		value, charset string
		mode           Mode
	}{
		{"proxy-private\x00", "", ModeOracle},
		{"proxy-private\xff", "", ModeOracle},
		{"private测试", "ascii", ModeOracle},
		{testProxyUser, "", ModeMySQL},
	} {
		_, err := Normalize(Config{User: testUser, ProxyUser: tc.value, Mode: tc.mode, Session: SessionOptions{Charset: tc.charset}})
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
