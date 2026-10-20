package config

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

func TestPlatformTransportConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Network: "udp", Addr: "localhost:2881"},
		{Network: networkUnix}, {Network: networkUnix, Addr: "bad\x00path"},
		{Network: networkUnix, Addr: platformTestSocket, Routing: RoutingOptions{Endpoints: []Endpoint{{Addr: "localhost:2881"}}}},
		{Network: networkUnix, Addr: platformTestSocket, Discovery: DiscoveryOptions{Service: "service", Source: func(context.Context) (Topology, error) { return Topology{}, nil }}},
		{Network: networkUnix, Addr: platformTestSocket, TLSConfig: &tls.Config{}},
		{Network: networkPipe, Addr: "ordinary-file"}, {Network: networkPipe, Addr: `\\.\pipe\`},
		{Network: networkPipe, Addr: `\\?\pipe\oceanbase`},
		{Network: networkPipe, Addr: `\\.\pipe\..\ordinary-file`},
	} {
		cfg.User = testUser
		if _, err := Normalize(cfg); err == nil {
			t.Fatalf("accepted invalid network %s", cfg.Network)
		}
	}
}

func TestPlatformTransportDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://user@localhost/?network=unix&socket=%2Frun%2Fob.sock")
	if err != nil || cfg.Network != networkUnix || cfg.Addr != "/run/ob.sock" {
		t.Fatalf("socket DSN: %s %s %v", cfg.Network, cfg.Addr, err)
	}
	if _, err = Normalize(cfg); err != nil {
		t.Fatal("normalized IPC config cannot create Connector", err)
	}
	for _, query := range []string{"socket=%2Frun%2Fob.sock", "network=unix", "network=unix&network=pipe&socket=x", "network=tcp&socket=x", "network=pipe&socket=regular-file"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?" + query); err == nil {
			t.Fatalf("invalid socket query %s", query)
		}
	}
}

func TestPlatformTransportPipeUTF8(t *testing.T) {
	for _, invalid := range []string{"\xff", "\xc3", "\xc0\xaf", "\xed\xa0\x80", "\xf4\x90\x80\x80"} {
		for _, addr := range []string{`\\.\pipe\oceanbase-` + invalid, `\\db-` + invalid + `\pipe\oceanbase`} {
			t.Run(fmt.Sprintf("%x", addr), func(t *testing.T) {
				_, err := Normalize(Config{User: testUser, Network: networkPipe, Addr: addr})
				var config *ConfigError
				if !errors.As(err, &config) || config.Field != configAddrField {
					t.Errorf("invalid UTF-8 pipe Config accepted: %v", err)
				}
				_, err = ParseDSN("oceanbase://user@localhost/?network=pipe&socket=" + url.QueryEscape(addr))
				if !errors.As(err, &config) || config.Field != configAddrField {
					t.Errorf("invalid UTF-8 pipe DSN accepted: %v", err)
				}
			})
		}
	}
	for _, addr := range []string{`\\.\pipe\数据库-😀-�`, `\\数据库\pipe\oceanbase`} {
		cfg, err := ParseDSN("oceanbase://user@localhost/?network=pipe&socket=" + url.QueryEscape(addr))
		if err != nil || cfg.Addr != addr {
			t.Fatal("Unicode pipe DSN changed its endpoint", err)
		}
		if _, err = Normalize(cfg); err != nil {
			t.Fatal("Unicode pipe Config rejected", err)
		}
	}
}
