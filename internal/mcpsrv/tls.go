package mcpsrv

import (
	"crypto/tls"
	"crypto/x509"
)

func tlsCfg(pool *x509.CertPool, min uint16) *tls.Config {
	if min == 0 {
		min = tls.VersionTLS12
	}
	return &tls.Config{MinVersion: min, RootCAs: pool}
}
