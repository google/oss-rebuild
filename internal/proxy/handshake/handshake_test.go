// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package handshake

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
)

const exampleHost = "example.com"

// generateRootLeafCA issues a self-signed leaf CA for the given host.
func generateRootLeafCA(host string) (*tls.Certificate, error) {
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2394),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.Errorf("failed to generate key: %v", err)
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return nil, errors.Errorf("failed to create CA: %v", err)
	}
	ca := new(tls.Certificate)
	ca.Certificate = append(ca.Certificate, caBytes)
	ca.PrivateKey = priv
	if ca.Leaf, err = x509.ParseCertificate(caBytes); err != nil {
		return nil, errors.Errorf("failed to parse CA leaf: %v", err)
	}
	return ca, nil
}

func TestPeekClientHello(t *testing.T) {
	// Create pair of connections local to the machine.
	server, client := net.Pipe()
	defer client.Close()

	ca, err := generateRootLeafCA(exampleHost)
	if err != nil {
		t.Fatalf("Failed to generate CA")
	}
	// Start server in separate goroutine.
	serverErr := make(chan error)
	go func() {
		defer server.Close()
		// Peek at the ClientHello.
		peeked, h, _ := PeekClientHello(server)
		if h.ServerName != exampleHost {
			serverErr <- errors.Errorf("Bad ClientHello metadata: got %s, expected %s", h.ServerName, exampleHost)
			return
		}
		tlsServer := tls.Server(peeked, &tls.Config{
			ServerName:   exampleHost,
			Certificates: []tls.Certificate{*ca},
		})
		if err := tlsServer.Handshake(); err != nil {
			serverErr <- errors.Errorf("Failed server handshake: %v", err)
			return
		}
		if !tlsServer.ConnectionState().HandshakeComplete {
			serverErr <- errors.Errorf("Failed server handshake")
			return
		}
		req, err := http.ReadRequest(bufio.NewReader(tlsServer))
		if err != nil {
			serverErr <- errors.Errorf("ReadRequest failed: %v", err)
			return
		}
		if req.Host != exampleHost {
			serverErr <- errors.Errorf("Host mismatch: %v", err)
			return
		}
		serverErr <- nil
	}()

	// Try to connect from client and send a simple HTTP request.
	roots := x509.NewCertPool()
	roots.AddCert(ca.Leaf)
	tlsClient := tls.Client(client, &tls.Config{ServerName: exampleHost, RootCAs: roots})
	if err := tlsClient.Handshake(); err != nil {
		t.Fatalf("Failed client handshake: %v", err)
	}
	if !tlsClient.ConnectionState().HandshakeComplete {
		t.Fatalf("Failed client handshake")
	}
	req, err := http.NewRequest("", "https://example.com", http.NoBody)
	if err != nil {
		t.Fatalf("Request construction failed: %v", err)
	}
	if err := req.Write(tlsClient); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("Server error: %v", err)
	}
}

func TestPeekClientHello_NotHttp(t *testing.T) {
	// Create pair of connections local to the machine.
	server, client := net.Pipe()
	defer client.Close()

	// Start server in separate goroutine.
	serverErr := make(chan error)
	go func() {
		defer server.Close()
		// Peek at the ClientHello.
		_, _, err := PeekClientHello(server)
		if err != nil {
			serverErr <- errors.Errorf("Bad ClientHello: %v", err)
			return
		}
		serverErr <- nil
	}()

	// Try to connect from client and send a non-HTTP request.
	_, err := client.Write([]byte("This is not a TLS Request\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	err = <-serverErr
	if err == nil || !strings.Contains(err.Error(), "tls: first record does not look like a TLS handshake") {
		t.Fatalf("Expected handshake error, got %v", err)
	}
}
