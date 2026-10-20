package config

import (
	"strings"
	"testing"
)

func TestAuthPluginGSSOptions(t *testing.T) {
	for _, spn := range []string{"bad\x00spn", "\xff", strings.Repeat("x", 1025)} {
		if _, err := Normalize(Config{User: testUser, Auth: AuthOptions{ServicePrincipalName: spn}}); err == nil {
			t.Fatal("accepted invalid SPN override")
		}
	}
	cfg, err := ParseDSN("oceanbase://user@localhost/?servicePrincipalName=service%2Fdb.example")
	if err != nil || cfg.Auth.ServicePrincipalName != "service/db.example" {
		t.Fatal("SPN option", err)
	}
}
