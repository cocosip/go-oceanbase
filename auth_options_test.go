package oceanbase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthExampleCredentials(t *testing.T) {
	t.Setenv("OB_USER", "rotating@tenant")
	t.Setenv("OB_PASSWORD", "auth-input-secret")
	t.Setenv("OB_PROXY_USER", "")
	got, err := authExampleCredentials(context.Background())
	if err != nil || got.User != "rotating@tenant" || got.Password != "auth-input-secret" || got.ProxyUser != "" {
		t.Fatal("environment credentials unavailable", err)
	}
	t.Setenv("OB_PASSWORD", "")
	t.Setenv("OB_PROXY_USER", "business")
	got, err = authExampleCredentials(context.Background())
	if err != nil || got.Password != "" || got.ProxyUser != "business" {
		t.Fatal("empty password or proxy identity lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = authExampleCredentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("credential source ignored cancellation", err)
	}
	for _, missing := range []string{"OB_USER", "OB_PASSWORD"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("OB_USER", "rotating@tenant")
			t.Setenv("OB_PASSWORD", "auth-input-secret")
			if err := os.Unsetenv(missing); err != nil {
				t.Fatal(err)
			}
			_, err := authExampleCredentials(context.Background())
			if err == nil || strings.Contains(err.Error(), "auth-input-secret") {
				t.Fatal("missing input accepted or disclosed", err)
			}
		})
	}
	t.Setenv("OB_USER", "")
	if _, err = authExampleCredentials(context.Background()); err == nil {
		t.Fatal("empty user accepted")
	}
}

func TestAuthExampleRSAFile(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKIXPublicKey(&other.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: publicDER})
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{
		{"pkix", valid, true},
		{"whitespace", append(bytes.Clone(valid), '\n', '\t'), true},
		{"no-pem", []byte("auth-input-secret"), false},
		{"trailing", append(bytes.Clone(valid), []byte("auth-input-secret")...), false},
		{"leading", append([]byte("auth-input-secret\n"), valid...), false},
		{"multiple", append(bytes.Clone(valid), valid...), false},
		{"skipped-block", append([]byte("-----BEGIN PUBLIC KEY-----\ninvalid!\n-----END PUBLIC KEY-----\n"), valid...), false},
		{"headers", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Headers: map[string]string{"Comment": "test"}, Bytes: publicDER}), false},
		{"malformed", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: []byte{1, 2}}), false},
		{"wrong-type", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: otherDER}), false},
		{"private-key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "server-public.pem")
			if err := os.WriteFile(file, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := authExampleRSAKey(file)
			if !tc.ok {
				if err == nil || got != nil || strings.Contains(err.Error(), "auth-input-secret") {
					t.Fatal("bad key input accepted or disclosed", err)
				}
				return
			}
			if err != nil || got == nil || got.N.Cmp(key.N) != 0 || got.E != key.E {
				t.Fatal("public key changed", err)
			}
			if _, err = NewConnector(Config{User: testUser, Auth: AuthOptions{RSAKey: got}}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err = authExampleRSAKey(filepath.Join(t.TempDir(), "missing.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file error lost", err)
	}
}
