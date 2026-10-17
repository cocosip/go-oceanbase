package oceanbase

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
)

const testSharedMemoryName = "MYSQL"

func TestPlatformTransportSharedMemoryConfig(t *testing.T) {
	for _, name := range []string{testSharedMemoryName, `Local\MYSQL`, `Global\MYSQL`, "数据库", strings.Repeat("数", 220), strings.Repeat("😀", 110)} {
		cfg, err := ParseDSN("oceanbase://app@localhost/?network=sharedmemory&socket=" + url.QueryEscape(name))
		if err != nil || cfg.Network != networkSharedMemory || cfg.Addr != name {
			t.Fatal("shared memory DSN rejected", err)
		}
		seen := false
		cfg.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			seen = network == networkSharedMemory && addr == name
			return nil, context.Canceled
		}
		created, err := NewConnector(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = created.Connect(context.Background()); !errors.Is(err, context.Canceled) || !seen {
			t.Fatal("shared memory callback identity lost", err)
		}
	}
	for _, name := range []string{"", "shared\x00name", " a", "a ", `a\b`, `Global\`, `Local\a\b`, `Local\Global\MYSQL`, "bad\xff", strings.Repeat("a", 221), strings.Repeat("😀", 111)} {
		if _, err := NewConnector(Config{User: testUser, Network: networkSharedMemory, Addr: name}); err == nil {
			t.Fatal("invalid shared memory name accepted")
		}
	}
	for _, cfg := range []Config{
		{TLSConfig: withoutServerName(t)},
		{Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: "shared.invalid:2881"}}}},
		{Discovery: DiscoveryOptions{Service: "shared-service", Source: func(context.Context) (Topology, error) { return Topology{}, nil }}},
	} {
		cfg.User, cfg.Network, cfg.Addr = testUser, networkSharedMemory, testSharedMemoryName
		if _, err := NewConnector(cfg); err == nil {
			t.Fatal("shared memory bypassed IPC policy")
		}
	}
}
