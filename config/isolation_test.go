package config_test

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"math/big"
	"net"
	"slices"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
)

func TestNormalizeIsolation(t *testing.T) {
	const mutated = "changed"
	if slices.Contains(sql.Drivers(), "oceanbase") {
		t.Fatal("importing config registered the SQL driver")
	}
	calls := 0
	variables := map[string]string{"time_zone": "+00:00"}
	initSQL := []string{"SET @a=1"}
	endpoints := []config.Endpoint{{Addr: "db.example:2881"}}
	protos := []string{"original"}
	certDER := []byte{1, 2, 3}
	roots := x509.NewCertPool()
	tlsConfig := &tls.Config{ServerName: "db.example", NextProtos: protos, RootCAs: roots, Certificates: []tls.Certificate{{Certificate: [][]byte{certDER}}}}
	key := &rsa.PublicKey{N: new(big.Int).Lsh(big.NewInt(1), 2048), E: 65537}
	plugins := map[string]config.AuthFactory{"custom": func() config.AuthPlugin { calls++; return nil }}
	cfg, err := config.Normalize(config.Config{
		Credentials: func(context.Context) (config.Credentials, error) {
			calls++
			return config.Credentials{User: "app"}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) { calls++; return nil, context.Canceled },
		Auth:        config.AuthOptions{RSAKey: key, Plugins: plugins},
		Session:     config.SessionOptions{Variables: variables, InitSQL: initSQL},
		Routing:     config.RoutingOptions{Endpoints: endpoints},
		TLSConfig:   tlsConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	variables["time_zone"], initSQL[0], endpoints[0].Addr, protos[0] = mutated, mutated, mutated, mutated
	tlsConfig.ServerName, certDER[0] = mutated, 9
	delete(plugins, "custom")
	key.N.SetInt64(1)
	key.E = 3
	if cfg.Session.Variables["time_zone"] != "+00:00" || cfg.Session.InitSQL[0] != "SET @a=1" || cfg.Routing.Endpoints[0].Addr != "db.example:2881" || cfg.TLSConfig.ServerName != "db.example" || cfg.TLSConfig.NextProtos[0] != "original" || cfg.TLSConfig.Certificates[0].Certificate[0][0] != 1 || cfg.TLSConfig.RootCAs == roots || cfg.Auth.Plugins["custom"] == nil || cfg.Auth.RSAKey.N.BitLen() != 2049 || cfg.Auth.RSAKey.E != 65537 {
		t.Fatal("Normalize retained mutable caller configuration")
	}
	_, err = config.Normalize(config.Config{User: "app", Discovery: config.DiscoveryOptions{
		Service: "service", Source: func(context.Context) (config.Topology, error) { calls++; return config.Topology{}, nil },
	}})
	if err != nil || calls != 0 {
		t.Fatal("Normalize invoked a runtime callback", err, calls)
	}
}
