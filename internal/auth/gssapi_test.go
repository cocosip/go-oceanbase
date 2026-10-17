package auth

import (
	"errors"
	"strings"
	"testing"
)

const testGSSPrincipal = "service/db.example"

func TestAuthPluginGSSSeed(t *testing.T) {
	for _, tc := range []struct{ seed, override, spn, mechanism string }{
		{"service/db.example\x00Kerberos\x00", "", testGSSPrincipal, "Kerberos"},
		{"service/db.example\x00\x00", "", testGSSPrincipal, "Kerberos"},
		{"service/db.example\x00negotiate\x00\x00", "", testGSSPrincipal, "Negotiate"},
		{"\x00NTLM\x00", "pinned/service", "pinned/service", "NTLM"},
	} {
		spn, mechanism, err := GSSAPISeed([]byte(tc.seed), tc.override)
		if err != nil || spn != tc.spn || mechanism != tc.mechanism {
			t.Fatalf("GSS seed: %q %q %v", spn, mechanism, err)
		}
	}
	for _, seed := range []string{"", "service", "service\x00Kerberos", "\x00\x00", "service\x00Kerberos\x00extra", "service\x00Kerberos\x00\x00\x00", "\xff\x00Kerberos\x00", strings.Repeat("x", 1025) + "\x00\x00"} {
		if _, _, err := GSSAPISeed([]byte(seed), ""); !errors.Is(err, ErrInvalidChallenge) {
			t.Fatal("accepted invalid GSS seed", err)
		}
	}
	if _, _, err := GSSAPISeed([]byte("service\x00CredSSP\x00"), ""); !errors.Is(err, ErrGSSMechanism) {
		t.Fatal("unrecognized authentication mechanism", err)
	}
}
