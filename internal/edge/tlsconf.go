package edge

import (
	"crypto/tls"
	"crypto/x509"
)

func tlsConfig(pool *x509.CertPool) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
}
