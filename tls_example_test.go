package oceanbase

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

func ExampleNewConnector_mutualTLS() {
	// The application owns file loading and private-key access; the driver receives TLS objects.
	policy, err := tlsExampleConfig("ca.pem", "client.pem", "client-key.pem", "oceanbase.example.com")
	if err != nil {
		fmt.Println(err)
		return
	}
	connector, err := NewConnector(Config{
		Addr: "oceanbase.example.com:2881", User: "app@tenant", TLSConfig: policy,
	})
	fmt.Println(connector != nil, err == nil)
	// This example requires application-provided files and is compiled without opening a connection.
}

// tlsExampleConfig is application example code, not a driver certificate-store API.
func tlsExampleConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no valid CA certificate")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12, ServerName: serverName,
		RootCAs: roots, Certificates: []tls.Certificate{certificate},
	}, nil
}
