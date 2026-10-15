// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cert

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
)

const (
	cert1PEM = `-----BEGIN CERTIFICATE-----
MIIBfjCCASOgAwIBAgIUU/kNSwqhwRdAWGc8eS9hMML+GAcwCgYIKoZIzj0EAwIw
FDESMBAGA1UEAwwJVGVzdCBDQSAxMB4XDTI2MTAxNTE5MDkwN1oXDTI3MTAxNTE5
MDkwN1owFDESMBAGA1UEAwwJVGVzdCBDQSAxMFkwEwYHKoZIzj0CAQYIKoZIzj0D
AQcDQgAEolSx46efu7J5YkjrQNWW5GKabJPjEGCLD2yDjpqqxQ8NuFpa61MXPF+J
qziau44m/nYqTctEnVJ5bcyBfDu8RaNTMFEwHQYDVR0OBBYEFKh1+roHmTVXhKzv
21OqJ7//aXbOMB8GA1UdIwQYMBaAFKh1+roHmTVXhKzv21OqJ7//aXbOMA8GA1Ud
EwEB/wQFMAMBAf8wCgYIKoZIzj0EAwIDSQAwRgIhALMKUE+WalbRU28Rd8xpKmMA
qusoc4MIvcRqoUMVRV/OAiEAx172hd6LnW5Dc5WM1aEPeSqnDBhbKYJb3mqBAIo5
Kao=
-----END CERTIFICATE-----`

	cert2PEM = `-----BEGIN CERTIFICATE-----
MIIBfTCCASOgAwIBAgIUGHfbjb/50kZckSIkfIemOPyUhw0wCgYIKoZIzj0EAwIw
FDESMBAGA1UEAwwJVGVzdCBDQSAyMB4XDTI2MTAxNTE5MDkyOVoXDTI3MTAxNTE5
MDkyOVowFDESMBAGA1UEAwwJVGVzdCBDQSAyMFkwEwYHKoZIzj0CAQYIKoZIzj0D
AQcDQgAEozG2CBrKgL+kvAGwSsykHx1xrM7pQmuHUiXlQCSKWQOCkDR1nwqvbebR
lLIU4uEZMg0KzJ/gxjkXNtHiu3TtvqNTMFEwHQYDVR0OBBYEFFSvuuHK7lBcL1Yt
/kaljLzvdn29MB8GA1UdIwQYMBaAFFSvuuHK7lBcL1Yt/kaljLzvdn29MA8GA1Ud
EwEB/wQFMAMBAf8wCgYIKoZIzj0EAwIDSAAwRQIgIdZ5gVEij2ndxvZhjcWxl/LV
6iHBY/nKtUFSuoqfQ3ICIQCjdLzsogAABMyzbYr34aicB8EBs+9OU/Vy3mmhFwWj
Zw==
-----END CERTIFICATE-----`
)

func parseCert(t *testing.T, pemStr string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate failed: %v", err)
	}
	return c
}

func verifyAliases(t *testing.T, data []byte, wantAliases []string) {
	t.Helper()
	ks := keystore.New()
	if err := ks.Load(bytes.NewReader(data), nil); err != nil {
		t.Fatalf("ks.Load failed: %v", err)
	}
	for _, alias := range wantAliases {
		if !ks.IsTrustedCertificateEntry(alias) {
			t.Errorf("missing expected alias %q", alias)
		}
	}
}

func TestToJKS(t *testing.T) {
	c1, c2 := parseCert(t, cert1PEM), parseCert(t, cert2PEM)
	tests := []struct {
		name        string
		certs       []*x509.Certificate
		wantAliases []string
	}{
		{
			name:        "single cert",
			certs:       []*x509.Certificate{c1},
			wantAliases: []string{"proxy"},
		},
		{
			name:        "multiple certs",
			certs:       []*x509.Certificate{c1, c2, c1},
			wantAliases: []string{"proxy", "ca-1", "ca-2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := ToJKS(tc.certs...)
			if err != nil {
				t.Fatalf("ToJKS failed: %v", err)
			}
			verifyAliases(t, data, tc.wantAliases)
		})
	}
}

func TestPEMToJKS(t *testing.T) {
	tests := []struct {
		name        string
		bundle      string
		wantAliases []string
	}{
		{
			name:        "valid bundle with comments",
			bundle:      "# leading comment\n" + cert1PEM + "\n# middle comment\n" + cert2PEM,
			wantAliases: []string{"proxy", "ca-1"},
		},
		{
			name:        "empty bundle",
			bundle:      "",
			wantAliases: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := PEMToJKS([]byte(tc.bundle))
			if err != nil {
				t.Fatalf("PEMToJKS failed: %v", err)
			}
			verifyAliases(t, data, tc.wantAliases)
		})
	}
}
