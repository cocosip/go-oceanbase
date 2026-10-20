package connect

import (
	"bytes"
	"context"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/authentication"
)

const authPasswordCharsetTest = "LATIN1"

func TestAuthPluginPasswordCharset(t *testing.T) {
	for _, name := range []string{authentication.Native, authentication.Ed25519, authentication.PAM, authentication.Clear} {
		c, err := config.Normalize(config.Config{User: testUser, Auth: config.AuthOptions{PasswordCharset: authPasswordCharsetTest}})
		if err != nil {
			t.Fatal(err)
		}
		plugin, err := authentication.New(c.Auth, name)
		if err != nil {
			t.Fatal(err)
		}
		challenge := config.AuthChallenge{Nonce: []byte("12345678901234567890"), TLSVerified: true}
		var want []byte
		switch name {
		case authentication.Native:
			want, _ = auth.Native("\xe9", challenge.Nonce)
		case authentication.Ed25519:
			challenge.Nonce = make([]byte, 32)
			want, _ = auth.Ed25519("\xe9", challenge.Nonce)
		case authentication.PAM:
			challenge.Nonce = []byte("\x04Password: ")
			want = []byte{0xe9, 0}
		case authentication.Clear:
			want = []byte{0xe9, 0}
		}
		got, err := authentication.Response(context.Background(), plugin, name, config.Credentials{Password: "é"}, challenge)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s response: %x %v", name, got, err)
		}
	}
	if _, err := config.Normalize(config.Config{User: testUser, Auth: config.AuthOptions{PasswordCharset: "no-such-charset"}}); err == nil {
		t.Fatal("invalid password charset accepted")
	}
}
