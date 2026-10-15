// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package cert provides certificate generation and formatting interfaces.
package cert

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
)

// GenerateCA generates a ca certificate.
func GenerateCA() *tls.Certificate {
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2394),
		Subject: pkix.Name{
			CommonName: "OSS Rebuild Proxy",
			Locality:   []string{"New York City"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	// TODO: Switch to ed25519.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("failed to generate key: %v", err)
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		log.Fatalf("failed to create CA: %v", err)
	}
	ca := new(tls.Certificate)
	ca.Certificate = append(ca.Certificate, caBytes)
	ca.PrivateKey = priv
	if ca.Leaf, err = x509.ParseCertificate(caBytes); err != nil {
		log.Fatalf("failed to parse CA leaf: %v", err)
	}
	return ca
}

// ToPEM encodes a certificate to PEM format.
func ToPEM(cert *x509.Certificate) []byte {
	b := new(bytes.Buffer)
	pem.Encode(b, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	return b.Bytes()
}

// ToJKS generates a Java KeyStore containing the provided certificates.
func ToJKS(certs ...*x509.Certificate) ([]byte, error) {
	f := keystore.New()
	count := 0
	for _, c := range certs {
		if c == nil {
			continue
		}
		alias := "proxy"
		if count > 0 {
			alias = fmt.Sprintf("ca-%d", count)
		}
		count++
		err := f.SetTrustedCertificateEntry(alias, keystore.TrustedCertificateEntry{
			CreationTime: time.Now(),
			Certificate: keystore.Certificate{
				Type:    "X.509",
				Content: c.Raw,
			},
		})
		if err != nil {
			return nil, err
		}
	}
	jksBuf := new(bytes.Buffer)
	if err := f.Store(jksBuf, []byte{}); err != nil {
		return nil, err
	}
	return jksBuf.Bytes(), nil
}

// PEMToJKS parses all PEM certificates in the provided bundle and writes them to a Java KeyStore.
func PEMToJKS(pemBundle []byte) ([]byte, error) {
	var certs []*x509.Certificate
	for block, rest := pem.Decode(pemBundle); block != nil; block, rest = pem.Decode(rest) {
		if block.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(block.Bytes); err == nil {
				certs = append(certs, c)
			}
		}
	}
	return ToJKS(certs...)
}
