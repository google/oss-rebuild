// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/proxy/cert"
	"github.com/google/oss-rebuild/pkg/proxy/policy"
)

func TestApplyNetworkPolicy(t *testing.T) {
	tests := []struct {
		name     string
		mode     PolicyMode
		policy   policy.Policy
		url      string
		wantResp int
	}{
		{
			name: "EnforcementMode passes compliant request through",
			mode: EnforcementMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
						HostMatch: policy.FullMatch,
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/compliance",
			wantResp: http.StatusOK,
		},
		{
			name: "EnforcementMode rejects non-compliant request",
			mode: EnforcementMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
						HostMatch: policy.FullMatch,
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/non/compliant/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "DisabledMode passes compliant request through",
			mode: DisabledMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
						HostMatch: policy.FullMatch,
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/compliance",
			wantResp: http.StatusOK,
		},
		{
			name: "DisabledMode passes non-compliant request through",
			mode: DisabledMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
						HostMatch: policy.FullMatch,
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/non/compliant/path",
			wantResp: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxyService := NewTransparentProxyService(NewTransparentProxyServer(false), nil, tc.mode, TransparentProxyServiceOpts{
				Policy:      &tc.policy,
				SkipLogging: true,
			})
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)

			_, gotResp := proxyService.ApplyNetworkPolicy(req, nil)
			// nil response means request allowed through proxy. Assume 200 status.
			if gotResp == nil && tc.wantResp != http.StatusOK {
				t.Errorf("ApplyNetworkPolicy returned an unexpected response code %v, want %v", http.StatusOK, tc.wantResp)
			}
			if gotResp != nil && tc.wantResp != gotResp.StatusCode {
				t.Errorf("ApplyNetworkPolicy returned an unexpected response code %v, want %v", gotResp.StatusCode, tc.wantResp)
			}
		})
	}
}

func TestPolicyEndpoint(t *testing.T) {
	preservedPolicy := policy.Policy{
		AnyOf: []policy.Rule{
			&policy.URLMatchRule{
				Host:      "host.com",
				Path:      "/path",
				PathMatch: policy.PrefixMatch,
			},
		},
	}
	tests := []struct {
		name          string
		initialPolicy policy.Policy
		method        string
		body          []byte
		wantPolicy    policy.Policy
		wantResp      int
	}{
		{
			name:       "Empty PUT request creates empty policy",
			method:     http.MethodPut,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusOK,
		},
		{
			name:   "Valid PUT request updates Policy",
			method: http.MethodPut,
			body: []byte(`{
				"Policy": {
					"AnyOf": [
						{
							"host": "host.com",
							"path": "/path",
							"matchPathBy": "prefix",
							"ruleType": "URLMatchRule"
						}
					]
				}
			}`),
			wantPolicy: preservedPolicy,
			wantResp:   http.StatusOK,
		},
		{
			name:          "PUT request without rule_type returns StatusBadRequest and preserves policy",
			initialPolicy: preservedPolicy,
			method:        http.MethodPut,
			body: []byte(`{
				"Policy": {
					"AnyOf": [
						{
							"host": "host.com",
							"path": "/path",
							"matchPathBy": "prefix"
						}
					]
				}
			}`),
			wantPolicy: preservedPolicy,
			wantResp:   http.StatusBadRequest,
		},
		{
			name:          "PUT request with non-string ruleType returns StatusBadRequest and preserves policy",
			initialPolicy: preservedPolicy,
			method:        http.MethodPut,
			body: []byte(`{
				"Policy": {
					"AnyOf": [
						{
							"host": "host.com",
							"ruleType": 123
						}
					]
				}
			}`),
			wantPolicy: preservedPolicy,
			wantResp:   http.StatusBadRequest,
		},
		{
			name:          "Empty PUT request resets policy",
			initialPolicy: preservedPolicy,
			method:        http.MethodPut,
			body:          []byte(`{}`),
			wantPolicy:    policy.Policy{},
			wantResp:      http.StatusOK,
		},
		{
			name:       "HEAD request returns StatusMethodNotAllowed",
			method:     http.MethodHead,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "POST request returns StatusMethodNotAllowed",
			method:     http.MethodPost,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "PATCH request returns StatusMethodNotAllowed",
			method:     http.MethodPatch,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "DELETE request returns StatusMethodNotAllowed",
			method:     http.MethodDelete,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "CONNECT request returns StatusMethodNotAllowed",
			method:     http.MethodConnect,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "OPTIONS request returns StatusMethodNotAllowed",
			method:     http.MethodOptions,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
		{
			name:       "TRACE request returns StatusMethodNotAllowed",
			method:     http.MethodTrace,
			body:       []byte(`{}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusMethodNotAllowed,
		},
	}
	policy.RegisterRule("URLMatchRule", func() policy.Rule { return &policy.URLMatchRule{} })

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			initPolicy := tc.initialPolicy
			proxyService := NewTransparentProxyService(NewTransparentProxyServer(false), nil, "enforce", TransparentProxyServiceOpts{
				Policy: &initPolicy,
			})
			mux := http.NewServeMux()
			mux.HandleFunc("/policy", proxyService.policyHandler)
			server := httptest.NewServer(mux)
			defer server.Close()
			req, err := http.NewRequest(tc.method, server.URL+"/policy", bytes.NewBuffer(tc.body))
			if err != nil {
				t.Errorf("Error creating request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("Error making request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantResp {
				t.Errorf("Request returned unexpected response code. got %v, want %v", resp.StatusCode, tc.wantResp)
			}

			if len(proxyService.Policy.AnyOf) != len(tc.wantPolicy.AnyOf) {
				t.Errorf("Policy length does not match. got %d, want %d", len(proxyService.Policy.AnyOf), len(tc.wantPolicy.AnyOf))
			}

			for i := range proxyService.Policy.AnyOf {
				if !reflect.DeepEqual(proxyService.Policy.AnyOf[i], tc.wantPolicy.AnyOf[i]) {
					t.Errorf("Difference at index %d: %v vs %v", i, proxyService.Policy.AnyOf[i], tc.wantPolicy.AnyOf[i])
				}
			}
		})
	}
}

func TestProxyTLSShutdownWaitsForConnections(t *testing.T) {
	// ProxyTLS listens on its own, so reserve a free port for it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	proxyService := NewTransparentProxyService(NewTransparentProxyServer(false), nil, DisabledMode, TransparentProxyServiceOpts{SkipLogging: true})
	go proxyService.ProxyTLS(addr)
	dial := func() net.Conn {
		for range 100 {
			if c, err := net.Dial("tcp", addr); err == nil {
				return c
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("Dial %s failed", addr)
		return nil
	}
	// idle never sends a ClientHello, so its serveTLS stays in flight.
	idle := dial()
	defer idle.Close()
	// serveTLS closes SNI-less handshakes and accepts are FIFO, so once the
	// probe fails, idle has been accepted.
	probe := dial()
	defer probe.Close()
	probe.SetDeadline(time.Now().Add(3 * time.Second))
	if err := tls.Client(probe, &tls.Config{InsecureSkipVerify: true}).Handshake(); err == nil {
		t.Fatal("SNI-less handshake succeeded")
	}
	done := make(chan error, 1)
	go func() { done <- proxyService.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v with a connection in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	idle.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after the in-flight connection closed")
	}
}

func TestServeTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	ca := cert.GenerateCA()
	ConfigureGoproxyCA(ca)
	p := NewTransparentProxyServer(false)
	// The httptest cert covers example.com, so dial upstream for it.
	p.Tr = upstream.Client().Transport.(*http.Transport).Clone()
	p.Tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	proxyService := NewTransparentProxyService(p, ca, DisabledMode, TransparentProxyServiceOpts{SkipLogging: true})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			proxyService.serveTLS(c)
		}
	}()
	roots := x509.NewCertPool()
	roots.AddCert(ca.Leaf)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: "example.com", RootCAs: roots})
	if err != nil {
		t.Fatalf("TLS handshake through proxy: %v", err)
	}
	defer conn.Close()
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, err := io.ReadAll(resp.Body); err != nil || string(body) != "ok" {
		t.Errorf("body = %q, %v, want %q", body, err, "ok")
	}
}

func TestRejectUnhandledConnectPort(t *testing.T) {
	srv := httptest.NewServer(NewTransparentProxyServer(false))
	defer srv.Close()
	// An unrejected CONNECT dials its target before replying, so the target
	// must be live for the reject case to see a 200.
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer backend.Close()
	for _, tc := range []struct {
		name       string
		target     string
		wantReject bool
	}{
		{name: "RejectUnhandledPort", target: backend.Addr().String(), wantReject: true},
		{name: "AllowHTTPSPort", target: "127.0.0.1:443", wantReject: false},
		{name: "AllowIPv6HTTPPort", target: "[::1]:80", wantReject: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := c.Write([]byte("CONNECT " + tc.target + " HTTP/1.1\r\nHost: " + tc.target + "\r\n\r\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err == nil {
				defer resp.Body.Close()
			}
			if tc.wantReject {
				if err == nil && resp.StatusCode == http.StatusOK {
					t.Fatalf("CONNECT %s = 200 OK, want rejection", tc.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("CONNECT %s error = %v, want 200 OK", tc.target, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("CONNECT %s = %d, want 200 OK", tc.target, resp.StatusCode)
			}
		})
	}
}
