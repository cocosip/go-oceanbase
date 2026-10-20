package config

import (
	"context"
	"crypto/tls"
	"net/url"
	"strings"
	"testing"
)

func TestPlatformTransportSharedMemoryConfig(t *testing.T) {
	for _, name := range []string{testSharedMemoryName, `Local\MYSQL`, `Global\MYSQL`, "数据库", strings.Repeat("数", 220), strings.Repeat("😀", 110)} {
		cfg, err := ParseDSN("oceanbase://app@localhost/?network=sharedmemory&socket=" + url.QueryEscape(name))
		if err != nil || cfg.Network != networkSharedMemory || cfg.Addr != name {
			t.Fatal("shared memory DSN rejected", err)
		}
	}
	for _, name := range []string{"", "shared\x00name", " a", "a ", `a\b`, `Global\`, `Local\a\b`, `Local\Global\MYSQL`, "bad\xff", strings.Repeat("a", 221), strings.Repeat("😀", 111)} {
		if _, err := Normalize(Config{User: testUser, Network: networkSharedMemory, Addr: name}); err == nil {
			t.Fatal("invalid shared memory name accepted")
		}
	}
	for _, cfg := range []Config{
		{TLSConfig: &tls.Config{}},
		{Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: "shared.invalid:2881"}}}},
		{Discovery: DiscoveryOptions{Service: "shared-service", Source: func(context.Context) (Topology, error) { return Topology{}, nil }}},
	} {
		cfg.User, cfg.Network, cfg.Addr = testUser, networkSharedMemory, testSharedMemoryName
		if _, err := Normalize(cfg); err == nil {
			t.Fatal("shared memory bypassed IPC policy")
		}
	}
}
