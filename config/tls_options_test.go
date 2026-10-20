package config

import (
	"errors"
	"testing"
)

func TestTLSJavaDSNRejected(t *testing.T) {
	for _, name := range []string{"useSsl", "serverSslCert", "trustStore", "trustStorePassword", "keyStore", "keyStorePassword", "keyPassword", "keyStoreType", "trustStoreType", "enabledSslProtocolSuites", "enabledSslCipherSuites", "tlsSocketType",
		"trustCertificateKeyStoreUrl", "trustCertificateKeyStorePassword", "clientCertificateKeyStoreUrl", "clientCertificateKeyStorePassword", "enabledSSLProtocolSuites", "trustServerCertificate", "disableSslHostnameVerification"} {
		_, err := ParseDSN("oceanbase://app@localhost/?" + name + "=true")
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatal("Java TLS option or legacy alias silently accepted", name, err)
		}
	}
}
