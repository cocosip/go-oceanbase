package oceanbase

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

const authExamplePublicKeyPEM = "PUBLIC KEY"

func ExampleNewConnector_credentials() {
	connector, err := NewConnector(Config{
		Addr: os.Getenv("OB_ADDR"), Mode: ModeOracle,
		Credentials: authExampleCredentials,
	})
	fmt.Println(connector != nil, err == nil)
	// Supply OB_USER, OB_PASSWORD and optional OB_PROXY_USER in the application.
	// NewConnector snapshots configuration but does not call the credentials callback.
}

// authExampleCredentials is application code; the driver never reads environment credentials.
func authExampleCredentials(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	user, userSet := os.LookupEnv("OB_USER")
	password, passwordSet := os.LookupEnv("OB_PASSWORD")
	if !userSet || user == "" || !passwordSet {
		return Credentials{}, errors.New("OB_USER and OB_PASSWORD must be set")
	}
	return Credentials{User: user, Password: password, ProxyUser: os.Getenv("OB_PROXY_USER")}, nil
}

func ExampleNewConnector_pinnedRSA() {
	// Provision this trusted key separately; RSA password encryption does not authenticate TLS.
	key, err := authExampleRSAKey("server-public.pem")
	if err != nil {
		fmt.Println(err)
		return
	}
	connector, err := NewConnector(Config{
		Addr: os.Getenv("OB_ADDR"),
		Auth: AuthOptions{RSAKey: key}, Credentials: authExampleCredentials,
	})
	fmt.Println(connector != nil, err == nil)
	// Files and credentials belong to the application; this example does not connect.
}

// authExampleRSAKey parses a single PKIX RSA public key, not a certificate or private key.
func authExampleRSAKey(file string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	block, rest := pem.Decode(data)
	if block == nil || !bytes.HasPrefix(data, []byte("-----BEGIN PUBLIC KEY-----")) || bytes.Count(data, []byte("-----BEGIN ")) != 1 ||
		block.Type != authExamplePublicKeyPEM || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("expected one PKIX PUBLIC KEY PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key must be RSA")
	}
	return key, nil
}
