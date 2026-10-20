package configspec

import (
	"crypto/tls"
	"slices"
)

// CloneTLSConfig snapshots the mutable TLS configuration containers.
//
//nolint:staticcheck // Snapshot the caller legacy certificate map without changing TLS selection.
func CloneTLSConfig(source *tls.Config) *tls.Config {
	clone := source.Clone()
	if source.RootCAs != nil {
		clone.RootCAs = source.RootCAs.Clone()
	}
	if source.ClientCAs != nil {
		clone.ClientCAs = source.ClientCAs.Clone()
	}
	clone.NextProtos = slices.Clone(source.NextProtos)
	clone.CipherSuites = slices.Clone(source.CipherSuites)
	clone.CurvePreferences = slices.Clone(source.CurvePreferences)
	clone.EncryptedClientHelloConfigList = slices.Clone(source.EncryptedClientHelloConfigList)
	clone.Certificates = make([]tls.Certificate, len(source.Certificates))
	for i, cert := range source.Certificates {
		clone.Certificates[i] = cloneCertificate(cert)
	}
	if source.NameToCertificate != nil {
		clone.NameToCertificate = make(map[string]*tls.Certificate, len(source.NameToCertificate))
		for name, cert := range source.NameToCertificate {
			if cert == nil {
				clone.NameToCertificate[name] = nil
				continue
			}
			copyCert := cloneCertificate(*cert)
			clone.NameToCertificate[name] = &copyCert
		}
	}
	return clone
}

func cloneCertificate(cert tls.Certificate) tls.Certificate {
	cert.Certificate = cloneBytesList(cert.Certificate)
	cert.OCSPStaple = slices.Clone(cert.OCSPStaple)
	cert.SignedCertificateTimestamps = cloneBytesList(cert.SignedCertificateTimestamps)
	cert.SupportedSignatureAlgorithms = slices.Clone(cert.SupportedSignatureAlgorithms)
	return cert
}

func cloneBytesList(source [][]byte) [][]byte {
	if source == nil {
		return nil
	}
	clone := make([][]byte, len(source))
	for i, b := range source {
		clone[i] = slices.Clone(b)
	}
	return clone
}
