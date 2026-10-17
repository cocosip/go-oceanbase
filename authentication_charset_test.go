package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/auth"
)

const authPasswordCharsetTest = "LATIN1"

func TestAuthPluginPasswordCharset(t *testing.T) {
	for _, name := range []string{authNative, authEd25519, authPAM, authClear} {
		c, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PasswordCharset: authPasswordCharsetTest}})
		if err != nil {
			t.Fatal(err)
		}
		plugin, err := c.(*connector).newAuthPlugin(name)
		if err != nil {
			t.Fatal(err)
		}
		challenge := AuthChallenge{Nonce: []byte("12345678901234567890"), TLSVerified: true}
		var want []byte
		switch name {
		case authNative:
			want, _ = auth.Native("\xe9", challenge.Nonce)
		case authEd25519:
			challenge.Nonce = make([]byte, 32)
			want, _ = auth.Ed25519("\xe9", challenge.Nonce)
		case authPAM:
			challenge.Nonce = []byte("\x04Password: ")
			want = []byte{0xe9, 0}
		case authClear:
			want = []byte{0xe9, 0}
		}
		got, err := authResponse(context.Background(), plugin, name, Credentials{Password: "é"}, challenge)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s response: %x %v", name, got, err)
		}
	}
	if _, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PasswordCharset: "no-such-charset"}}); err == nil {
		t.Fatal("invalid password charset accepted")
	}
}

func TestAuthPluginOptionsDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://user@localhost/?allowOldPassword=true&passwordCharset=latin1")
	if err != nil || !cfg.Auth.AllowOldPassword || !strings.EqualFold(cfg.Auth.PasswordCharset, authPasswordCharsetTest) {
		t.Fatalf("auth DSN: %+v %v", cfg.Auth, err)
	}
	for _, value := range []string{"1", "TRUE", "false&allowOldPassword=true"} {
		if _, err = ParseDSN("oceanbase://user@localhost/?allowOldPassword=" + value); err == nil {
			t.Fatal("invalid bool accepted")
		}
	}
	_, err = ParseDSN("oceanbase://user@localhost/?passwordCharset=unknown")
	var config *ConfigError
	if !errors.As(err, &config) {
		t.Fatal("invalid charset", err)
	}
}
