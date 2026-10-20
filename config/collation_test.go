package config

import (
	"errors"
	"strings"
	"testing"
)

func TestCollationConfig(t *testing.T) {
	created, err := Normalize(Config{User: testUser, Session: SessionOptions{Collation: "UTF8MB4_BIN"}})
	if err != nil || created.Session.Collation != testCollationUTF8 {
		t.Fatal("collation not normalized", err)
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/?charset=GBK&collation=GBK_BIN")
	if err != nil || cfg.Session.Charset != localFilenameCharset || cfg.Session.Collation != testCollationGBK {
		t.Fatal("DSN session options lost", err)
	}
	for _, value := range []string{"utf8mb4_bin;SELECT private", "utf8mb4_bin\x00private", "utf8mb4_bin private", "utf8mb4_bin'private", "私有", "UTF8MB4_K", "utf8mb4_bin\xff", strings.Repeat("a", 129)} {
		_, err := Normalize(Config{User: testUser, Session: SessionOptions{Collation: value}})
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "Session.Collation" || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe collation accepted or exposed: %v", err)
		}
	}
	if _, err = Normalize(Config{User: testUser, Mode: ModeOracle, Session: SessionOptions{Collation: testCollationUTF8}}); err == nil {
		t.Fatal("Oracle collation silently accepted")
	}
}
