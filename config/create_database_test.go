package config

import (
	"errors"
	"strings"
	"testing"
)

func TestDSNCreateDatabase(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		cfg, err := ParseDSN("oceanbase://app@localhost/example?mode=mysql&createDatabaseIfMissing=" + value)
		if err != nil || cfg.CreateDatabaseIfMissing != (value == "true") {
			t.Fatalf("valid explicit creation policy rejected: %v", err)
		}
	}
	for _, dsn := range []string{
		"oceanbase://app@localhost/?createDatabaseIfMissing=true",
		"oceanbase://app@localhost/example?mode=oracle&createDatabaseIfMissing=true",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=1",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=TRUE",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=true&createDatabaseIfMissing=false",
	} {
		_, err := ParseDSN(dsn)
		var invalid *ConfigError
		if !errors.As(err, &invalid) || strings.Contains(err.Error(), "example") {
			t.Fatalf("invalid policy accepted or target leaked: %v", err)
		}
	}
}

func TestCreateDatabaseConfig(t *testing.T) {
	for _, cfg := range []Config{
		{User: testUser, CreateDatabaseIfMissing: true},
		{User: testUser, Mode: ModeOracle, Database: "private", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "private-create\xff", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "private-create\x00", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "私有", Session: SessionOptions{Charset: "ASCII"}, CreateDatabaseIfMissing: true},
	} {
		_, err := Normalize(cfg)
		var invalid *ConfigError
		if !errors.As(err, &invalid) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "私有") {
			t.Fatalf("invalid creation target accepted or leaked: %v", err)
		}
	}
	created, err := Normalize(Config{User: testUser})
	if err != nil || created.CreateDatabaseIfMissing {
		t.Fatal("database creation is not disabled by default", err)
	}
}
