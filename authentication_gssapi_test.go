package oceanbase

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAuthPluginGSSOptions(t *testing.T) {
	for _, spn := range []string{"bad\x00spn", "\xff", strings.Repeat("x", 1025)} {
		if _, err := NewConnector(Config{User: testUser, Auth: AuthOptions{ServicePrincipalName: spn}}); err == nil {
			t.Fatal("accepted invalid SPN override")
		}
	}
	cfg, err := ParseDSN("oceanbase://user@localhost/?servicePrincipalName=service%2Fdb.example")
	if err != nil || cfg.Auth.ServicePrincipalName != "service/db.example" {
		t.Fatal("SPN option", err)
	}
	created, err := NewConnector(Config{User: testUser})
	if err != nil {
		t.Fatal(err)
	}
	for _, mechanism := range []string{"NTLM", "Negotiate"} {
		plugin, e := created.(*connector).newAuthPlugin(testGSSPlugin)
		if e != nil {
			t.Fatal(e)
		}
		_, e = authResponse(context.Background(), plugin, testGSSPlugin, Credentials{User: testUser}, AuthChallenge{Nonce: []byte("service/localhost\x00" + mechanism + "\x00")})
		if !errors.Is(e, ErrTLSRequired) {
			t.Fatal("non-Kerberos GSS token without verified TLS", e)
		}
	}
}
