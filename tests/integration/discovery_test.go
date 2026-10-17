//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

// Each source needs a real service and a deployment-specific query returning tenant and logical role.
func TestDiscovery(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			prefix := strings.TrimSuffix(p.env, "_DSN")
			identitySQL := os.Getenv(prefix + "_DISCOVERY_IDENTITY_SQL")
			tenant := os.Getenv(prefix + "_DISCOVERY_TENANT")
			role := os.Getenv(prefix + "_DISCOVERY_ROLE")
			if identitySQL == "" || tenant == "" || (role != "readwrite" && role != "readonly") {
				if os.Getenv("OB_TEST_REQUIRE") == "1" {
					t.Fatal("discovery identity SQL, tenant and role required")
				}
				t.Skip("discovery identity proof not configured")
			}
			for _, kind := range []string{"TNS", "OCP"} {
				t.Run(kind, func(t *testing.T) {
					dsn := os.Getenv(prefix + "_" + kind + "_DSN")
					if dsn == "" {
						if os.Getenv("OB_TEST_REQUIRE") == "1" {
							t.Fatal("discovery source DSN required")
						}
						t.Skip("source not configured")
					}
					cfg, err := oceanbase.ParseDSN(dsn)
					if err != nil {
						failOperation(t, "discovery DSN", err)
					}
					if cfg.Discovery.Source == nil || (cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.mode) {
						t.Fatal("discovery source/mode mismatch")
					}
					wantRole := oceanbase.RoleReadWrite
					if role == "readonly" {
						wantRole = oceanbase.RoleReadOnly
					}
					if cfg.Routing.Role != wantRole {
						t.Fatal("discovery role constraint must match expected role")
					}
					// The deployment labels its entire logical service; the query below verifies that claim.
					source := cfg.Discovery.Source
					cfg.Discovery.Source = func(ctx context.Context) (oceanbase.Topology, error) {
						topology, err := source(ctx)
						for i := range topology.Endpoints {
							if topology.Endpoints[i].Role == oceanbase.RoleAny {
								topology.Endpoints[i].Role = wantRole
							}
						}
						return topology, err
					}
					cfg.Mode = p.mode
					connector, err := oceanbase.NewConnector(cfg)
					if err != nil {
						failOperation(t, "discovery connector", err)
					}
					db := sql.OpenDB(connector)
					db.SetMaxOpenConns(1)
					defer func() { _ = db.Close() }()
					var gotTenant, gotRole string
					if err = db.QueryRowContext(testContext(t), identitySQL).Scan(&gotTenant, &gotRole); err != nil {
						failOperation(t, "discovery identity", err)
					}
					if gotTenant != tenant || gotRole != role {
						t.Fatal("discovery crossed tenant or role")
					}
					if err = db.Close(); err != nil {
						failOperation(t, "discovery close", err)
					}
					t.Log("authenticated discovery source and same-session tenant/role proof passed; controlled topology changes remain a separate deployment check")
				})
			}
		})
	}
}
