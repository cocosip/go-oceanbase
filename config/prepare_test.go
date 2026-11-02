package config

import (
	"testing"
)

func TestClientPrepareConfig(t *testing.T) {
	if _, err := Normalize(Config{User: testUser, PrepareMode: 99}); err == nil {
		t.Fatal("invalid prepare mode")
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/db?prepareMode=client")
	if err != nil || cfg.PrepareMode != PrepareClient {
		t.Fatal(err)
	}
	if _, err = ParseDSN("oceanbase://app@localhost/db?prepareMode=unknown"); err == nil {
		t.Fatal("invalid DSN strategy")
	}
}

func TestEmulateUnsupportedPstmtsConfig(t *testing.T) {
	if cfg, err := Normalize(Config{User: testUser}); err != nil || cfg.EmulateUnsupportedPstmts {
		t.Fatalf("fallback default changed: %+v %v", cfg, err)
	}
	for _, value := range []string{dsnTrue, dsnFalse} {
		cfg, err := ParseDSN("oceanbase://app@localhost/db?emulateUnsupportedPstmts=" + value)
		if err != nil || cfg.EmulateUnsupportedPstmts != (value == dsnTrue) {
			t.Fatalf("fallback DSN %s: %+v %v", value, cfg, err)
		}
	}
	for _, dsn := range []string{
		"oceanbase://app@localhost/db?emulateUnsupportedPstmts=1",
		"oceanbase://app@localhost/db?emulateUnsupportedPstmts=true&emulateUnsupportedPstmts=false",
	} {
		if _, err := ParseDSN(dsn); err == nil {
			t.Fatalf("invalid fallback DSN accepted: %s", dsn)
		}
	}
}

func TestProxyChecksumConfiguration(t *testing.T) {
	if _, err := Normalize(Config{User: "app", Protocol: ProtocolOptions{ProxyChecksum: 255}}); err == nil {
		t.Fatal("invalid proxy policy")
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?proxyChecksum=required&ob20=off")
	if err != nil || cfg.Protocol.ProxyChecksum != FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
}
