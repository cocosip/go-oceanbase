package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDSNOptionNamesIgnoreCaseAcrossParsers(t *testing.T) {
	tests := []struct {
		name  string
		dsn   string
		check func(Config) bool
	}{
		{"connection", "connecttimeout=10s", func(cfg Config) bool { return cfg.ConnectTimeout == 10*time.Second }},
		{"session", "LoCaTiOn=Etc%2FGMT%2B8", func(cfg Config) bool { return cfg.Session.Location.String() == "Etc/GMT+8" }},
		{"authentication", "SeRvIcEpRiNcIpAlNaMe=service%2FDB.EXAMPLE", func(cfg Config) bool { return cfg.Auth.ServicePrincipalName == "service/DB.EXAMPLE" }},
		{"protocol", "CoMpReSsIoN=required", func(cfg Config) bool { return cfg.Protocol.Compression == FeatureRequired }},
		{"execution", "StAtEmEnTcAcHeSiZe=4", func(cfg Config) bool { return cfg.Execution.StatementCacheSize == 4 }},
		{"routing", "RoLe=readonly&AdDrEsSeS=localhost%3A2882", func(cfg Config) bool {
			return cfg.Routing.Role == RoleReadOnly && cfg.Routing.Endpoints[0].Addr == "localhost:2882"
		}},
		{"transport", "NeTwOrK=unix&SoCkEt=%2Ftmp%2Foceanbase.sock", func(cfg Config) bool { return cfg.Network == networkUnix && cfg.Addr == "/tmp/oceanbase.sock" }},
		{"discovery", "TnSfIlE=%2Ftmp%2Ftns&TnSaLiAs=app&DiScOvErYsErViCe=orders&DiScOvErYtImEoUt=2s", func(cfg Config) bool { return cfg.Discovery.RequestTimeout == 2*time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseDSN("oceanbase://app@localhost/db?" + tc.dsn)
			if err != nil || !tc.check(cfg) {
				t.Fatalf("DSN option was not parsed case-insensitively: cfg=%+v err=%v", cfg, err)
			}
		})
	}
}

func TestDSNConnectTimeoutOptionNameVariants(t *testing.T) {
	for _, name := range []string{"connectTimeout", "connecttimeout", "CONNECTTIMEOUT", "CoNnEcTtImEoUt"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseDSN("oceanbase://app@localhost/db?" + name + "=10s")
			if err != nil || cfg.ConnectTimeout != 10*time.Second {
				t.Fatalf("connect timeout option name %q was not accepted: %v", name, err)
			}
		})
	}
}

func TestDSNOptionCasePreservesValuesAndRejectsDuplicates(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@localhost/db?connecttimeout=10s&serviceprincipalname=service%2FDB.EXAMPLE")
	if err != nil || cfg.Auth.ServicePrincipalName != "service/DB.EXAMPLE" || cfg.ConnectTimeout != 10*time.Second {
		t.Fatalf("DSN values changed during key normalization: cfg=%+v err=%v", cfg, err)
	}
	for _, query := range []string{
		"connectTimeout=1s&connectTimeout=1s",
		"connectTimeout=1s&CONNECTTIMEOUT=1s",
		"CONNECTTIMEOUT=1s&cOnNeCtTiMeOuT=2s",
	} {
		if _, err := ParseDSN("oceanbase://app@localhost/db?" + query); err == nil {
			t.Fatalf("duplicate DSN option accepted: %s", query)
		}
	}
}

func TestDSNOptionCaseKeepsValidationAndSanitization(t *testing.T) {
	for _, query := range []string{"CoNnEcTtImEoUt=invalid", "UNKNOWNOPTION=secret-value"} {
		_, err := ParseDSN("oceanbase://app@localhost/db?" + query)
		if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "oceanbase://") {
			t.Fatalf("invalid DSN option was accepted or exposed: %s: %v", query, err)
		}
		var configErr *ConfigError
		if !errors.As(err, &configErr) {
			t.Fatalf("invalid DSN option did not return ConfigError: %v", err)
		}
	}
}
