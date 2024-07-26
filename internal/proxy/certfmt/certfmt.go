// Package certfmt provides certificate formatting interfaces.
package certfmt

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"time"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
)

// ToPEM encodes a certificate to PEM format.
func ToPEM(cert *x509.Certificate) []byte {
	b := new(bytes.Buffer)
	pem.Encode(b, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	return b.Bytes()
}

// ToJKS generates a Java KeyStore with the provided certificate.
func ToJKS(cert *x509.Certificate) ([]byte, error) {
	certBytes := ToPEM(cert)
	f := keystore.New()
	err := f.SetTrustedCertificateEntry("proxy", keystore.TrustedCertificateEntry{
		CreationTime: time.Now(),
		Certificate: keystore.Certificate{
			Type:    "X509",
			Content: certBytes,
		},
	})
	if err != nil {
		return nil, err
	}
	jksBuf := new(bytes.Buffer)
	if err := f.Store(jksBuf, []byte{}); err != nil {
		return nil, err
	}
	return jksBuf.Bytes(), nil
}
