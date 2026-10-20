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

func TestProxyChecksumConfiguration(t *testing.T) {
	if _, err := Normalize(Config{User: "app", Protocol: ProtocolOptions{ProxyChecksum: 255}}); err == nil {
		t.Fatal("invalid proxy policy")
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?proxyChecksum=required&ob20=off")
	if err != nil || cfg.Protocol.ProxyChecksum != FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
}
