//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

// Local discovery fixtures lead to real SQL; this does not certify a deployed OCP or HA cluster.
func TestMySQLDiscoverySources(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.name, func(t *testing.T) { testMySQLDiscoverySources(t, p) })
	}
}

func testMySQLDiscoverySources(t *testing.T, p profile) {
	t.Helper()
	cfg := profileConfig(t, p)
	direct := openProfile(t, p)
	ctx := testContext(t)
	const identityQuery = "SELECT TENANT_NAME,TENANT_ROLE FROM oceanbase.DBA_OB_TENANTS"
	var tenant, role string
	if err := direct.QueryRowContext(ctx, identityQuery).Scan(&tenant, &role); err != nil {
		failOperation(t, "direct discovery identity", err)
	}
	address := cfg.Addr
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		failOperation(t, "discovery address", err)
	}
	const service = "mysql-validation"
	file := filepath.Join(t.TempDir(), "tnsnames.ob")
	description := fmt.Sprintf("live=(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(HOST=%s)(PORT=%s)(TYPE=master)))(CONNECT_DATA=(SERVICE_NAME=%s)))", host, port, service)
	if err = os.WriteFile(file, []byte(description), 0600); err != nil {
		t.Fatal(err)
	}
	tns, err := oceanbase.NewTNSFileSource(file, "live")
	if err != nil {
		failOperation(t, "TNS source config", err)
	}
	body, err := json.Marshal(map[string]any{
		"ADDRESS_LIST": []any{map[string]any{"ADDRESS": []any{map[string]string{"HOST": host, "PORT": port, "TYPE": "master"}}}},
		"CONNECT_DATA": map[string]string{"SERVICE_NAME": service},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	ocp, err := oceanbase.NewOCPSource(server.URL, server.Client())
	if err != nil {
		failOperation(t, "OCP source config", err)
	}
	for _, source := range []struct {
		name   string
		source oceanbase.TopologySource
	}{{"TNS", tns}, {"OCP-fixture", ocp}} {
		t.Run(source.name, func(t *testing.T) {
			topology, err := source.source(testContext(t))
			if err != nil {
				t.Fatal("discovery source lookup", err)
			}
			t.Logf("fixture discovery service=%s endpoints=%+v", topology.Service, topology.Endpoints)
			cfg.Discovery = oceanbase.DiscoveryOptions{Source: source.source, Service: service}
			cfg.Routing.Endpoints = nil
			cfg.Routing.Role = oceanbase.RoleReadWrite
			cfg.DialContext = func(ctx context.Context, network, actual string) (net.Conn, error) {
				if actual != address {
					return nil, fmt.Errorf("unexpected discovery address")
				}
				return (&net.Dialer{}).DialContext(ctx, network, actual)
			}
			cfg.Addr = "source-must-select.invalid:2881"
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "discovery connector", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			var gotTenant, gotRole string
			if err = db.QueryRowContext(testContext(t), identityQuery).Scan(&gotTenant, &gotRole); err != nil {
				failOperation(t, "discovered database identity", err)
			}
			if gotTenant != tenant || gotRole != role {
				t.Fatal("discovery changed tenant or role", gotTenant, gotRole)
			}
		})
	}
}
