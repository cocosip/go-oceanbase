package config

import (
	"errors"
	"testing"
)

func TestDSNSkipVersionProbe(t *testing.T) {
	for _, value := range []string{dsnTrue, dsnFalse} {
		cfg, err := ParseDSN("oceanbase://app@localhost/?skipVersionProbe=" + value)
		if err != nil || cfg.Session.SkipVersionProbe != (value == dsnTrue) {
			t.Fatal("version probe policy rejected", err)
		}
		if _, err = Normalize(cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"skipVersionProbe=", "skipVersionProbe=TRUE", "skipVersionProbe=1", "skipVersionProbe=true&skipVersionProbe=false", "connectProxy=true"} {
		_, err := ParseDSN("oceanbase://app@localhost/?" + query)
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatal("invalid version policy accepted", query, err)
		}
	}
	created, err := Normalize(Config{User: testUser})
	if err != nil || created.Session.SkipVersionProbe {
		t.Fatal("version probing disabled by default", err)
	}
}
