//go:build gssapi && linux && !android && (amd64 || arm64 || 386)

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthPluginGSSSystemCredentialFailure(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "krb5.conf")
	if err := os.WriteFile(config, []byte("[libdefaults]\n default_realm = DRIVER.INVALID\n dns_lookup_realm = false\n dns_lookup_kdc = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRB5_CONFIG", config)
	t.Setenv("KRB5CCNAME", "FILE:"+filepath.Join(dir, "missing-cache"))
	t.Setenv("KRB5_CLIENT_KTNAME", "FILE:"+filepath.Join(dir, "missing-client-keytab"))
	c, err := NewGSSContext(testGSSPrincipal, "Kerberos")
	if c != nil {
		_ = c.Close()
		t.Fatal("acquired an identity from an absent cache/keytab")
	}
	if errors.Is(err, ErrGSSUnavailable) && os.Getenv("OB_TEST_GSS_REQUIRE") != "1" {
		t.Skip("system GSSAPI is not installed")
	}
	if err == nil || !strings.Contains(err.Error(), "acquire credential") || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), testGSSPrincipal) {
		t.Fatal("system credential failure not isolated/redacted", err)
	}
}

func TestAuthPluginGSSSystemOptions(t *testing.T) {
	if _, err := NewGSSContext(testGSSPrincipal, "NTLM"); !errors.Is(err, ErrGSSMechanism) {
		t.Fatal("Unix provider accepted an undeclared mechanism", err)
	}
	if _, err := NewGSSContext("bad\x00principal", "Kerberos"); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("invalid target reached native library", err)
	}
}

func TestAuthPluginGSSMissingLibrary(t *testing.T) {
	lookup, closeLibrary, err := openGSSLibrary(filepath.Join(t.TempDir(), "missing-gss-library.so"))
	if err == nil || lookup != nil || closeLibrary != nil {
		t.Fatal("failed native library load returned usable functions", err)
	}
}
