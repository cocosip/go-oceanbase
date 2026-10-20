package config

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	c, err := Normalize(Config{User: testUser})
	if err != nil {
		t.Fatal(err)
	}
	cfg := c
	if cfg.Addr != "127.0.0.1:2881" || cfg.Mode != ModeAuto || cfg.ConnectTimeout != 10*time.Second {
		t.Fatalf("unexpected connection defaults: addr=%q mode=%v timeout=%v", cfg.Addr, cfg.Mode, cfg.ConnectTimeout)
	}
	if cfg.Limits.MaxMessageBytes != 64<<20 || cfg.Limits.MaxLOBBytes != 16<<20 || cfg.Limits.FetchSize != 128 || cfg.Limits.MaxResultItems != 1024 || cfg.Limits.CleanupTimeout != time.Second {
		t.Fatalf("unexpected resource defaults: %+v", cfg.Limits)
	}
	if cfg.RowsAffectedMode != MatchedRows || cfg.ResetMode != ResetManaged || cfg.AllowMultiStatements || cfg.Logger != nil || cfg.TLSConfig != nil {
		t.Fatal("unsafe defaults")
	}
	if cfg.Session.Charset != testUTF8MB4 || cfg.Session.Location != time.UTC {
		t.Fatal("unstable charset/time defaults")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"missing user", Config{}},
		{"mode", Config{User: testUser, Mode: 99}},
		{"negative timeout", Config{User: testUser, ConnectTimeout: -1}},
		{"negative message limit", Config{User: testUser, Limits: Limits{MaxMessageBytes: -1}}},
		{"negative lob limit", Config{User: testUser, Limits: Limits{MaxLOBBytes: -1}}},
		{"negative fetch", Config{User: testUser, Limits: Limits{FetchSize: -1}}},
		{"negative result limit", Config{User: testUser, Limits: Limits{MaxResultItems: -1}}},
		{"negative cleanup", Config{User: testUser, Limits: Limits{CleanupTimeout: -1}}},
		{"insecure TLS", Config{User: testUser, TLSConfig: &tls.Config{InsecureSkipVerify: true}}},
		{"invalid counts", Config{User: testUser, RowsAffectedMode: 99}},
		{"oracle changed counts", Config{User: testUser, Mode: ModeOracle, RowsAffectedMode: ChangedRows}},
		{"invalid reset", Config{User: testUser, ResetMode: 99}},
		{"invalid address", Config{User: testUser, Addr: "host:0"}},
		{"invalid port", Config{User: testUser, Addr: "host:65536"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(tc.cfg)
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("want ConfigError, got %v", err)
			}
		})
	}
}
