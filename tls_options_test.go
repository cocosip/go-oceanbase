package oceanbase

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const tlsClientName = "oceanbase-app"
const tlsCertificatePEM = "CERTIFICATE"

func tlsClientFiles(t *testing.T, server *tls.Config) (string, string, string) {
	t.Helper()
	rootDER := server.Certificates[0].Certificate[0]
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture's root must authorize both uses; the shared server-only fixture stays unchanged.
	root.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	rootDER, err = x509.CreateCertificate(rand.Reader, root, root, root.PublicKey, server.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	server.Certificates[0].Certificate[0] = rootDER
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: tlsClientName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, server.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile, certFile, keyFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "client.pem"), filepath.Join(dir, "client-key.pem")
	for name, data := range map[string][]byte{
		caFile: pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: rootDER}),
		certFile: append(pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: tlsCertificatePEM, Bytes: rootDER})...),
		keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err = os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return caFile, certFile, keyFile
}

func TestTLSExampleFiles(t *testing.T) {
	_, server := testTLSConfig(t)
	caFile, certFile, keyFile := tlsClientFiles(t, server)
	badFile := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(badFile, []byte("not a certificate or key"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.pem")
	for _, files := range [][3]string{{missing, certFile, keyFile}, {badFile, certFile, keyFile}, {caFile, missing, keyFile}, {caFile, badFile, keyFile}, {caFile, certFile, missing}, {caFile, certFile, badFile}} {
		if cfg, err := tlsExampleConfig(files[0], files[1], files[2], testServerName); err == nil || cfg != nil {
			t.Fatal("example accepted invalid trust or client identity files", files, err)
		}
	}
}
