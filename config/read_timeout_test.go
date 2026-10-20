package config

import (
	"errors"
	"testing"
	"time"
)

func TestDSNReadTimeout(t *testing.T) {
	for _, value := range []string{"0s", "250ms", "2s"} {
		if _, err := ParseDSN("oceanbase://app@localhost/example?readTimeout=" + value); err != nil {
			t.Fatalf("valid read timeout rejected: %v", err)
		}
	}
	for _, value := range []string{"", "-1ns", "1", "never", "999999999999999999999h", "1s&readTimeout=2s"} {
		_, err := ParseDSN("oceanbase://app@localhost/example?readTimeout=" + value)
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid read timeout accepted: %v", err)
		}
	}
}

func TestReadTimeoutConfig(t *testing.T) {
	_, err := Normalize(Config{User: testUser, ReadTimeout: -time.Nanosecond})
	var invalid *ConfigError
	if !errors.As(err, &invalid) || invalid.Field != "ReadTimeout" {
		t.Fatal("negative read timeout accepted", err)
	}
	configured, err := Normalize(Config{User: testUser})
	if err != nil || configured.ReadTimeout != 0 {
		t.Fatal("read timeout is not disabled by default", err)
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/?readTimeout=250ms")
	if err != nil || cfg.ReadTimeout != 250*time.Millisecond {
		t.Fatal("read timeout duration lost", err)
	}
}
