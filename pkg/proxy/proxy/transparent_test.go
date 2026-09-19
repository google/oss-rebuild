// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/elazarl/goproxy"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/proxy/policy"
)

func TestApplyNetworkPolicy(t *testing.T) {
	proxyService := TransparentProxyService{}
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
			proxyService.Mode = tc.mode
			proxyService.Policy = &tc.policy
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
	tests := []struct {
		name       string
		method     string
		body       []byte
		wantPolicy policy.Policy
		wantResp   int
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
			wantPolicy: policy.Policy{
				AnyOf: []policy.Rule{
					&policy.URLMatchRule{
						Host:      "host.com",
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			wantResp: http.StatusOK,
		},
		{
			name:   "PUT request without rule_type returns StatusBadRequest",
			method: http.MethodPut,
			body: []byte(`{
				"Policy": {
					"AnyOf": [
						{
							"host": "host.com",
							"path": "/path",
							"matchPathBy": "prefix",
						}
					]
				}
			}`),
			wantPolicy: policy.Policy{},
			wantResp:   http.StatusBadRequest,
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
	proxyService := NewTransparentProxyService(NewTransparentProxyServer(false), nil, "enforce", TransparentProxyServiceOpts{
		Policy: &policy.Policy{},
	})
	policy.RegisterRule("URLMatchRule", func() policy.Rule { return &policy.URLMatchRule{} })
	mux := http.NewServeMux()
	mux.HandleFunc("/policy", proxyService.policyHandler)
	server := httptest.NewServer(mux)
	defer server.Close()
	url := server.URL + "/policy"

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, url, bytes.NewBuffer(tc.body))
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

// TestHandleUninterceptedConnect covers CONNECTs to ports that are not
// MITM-intercepted (anything other than 80/443). These bypass the OnRequest
// hooks, so policy and logging must be applied at the CONNECT/host level.
func TestHandleUninterceptedConnect(t *testing.T) {
	allowlist := policy.Policy{
		AnyOf: []policy.Rule{
			policy.URLMatchRule{
				Host:      "allowed.test",
				HostMatch: policy.FullMatch,
				Path:      "/",
				PathMatch: policy.PrefixMatch,
			},
		},
	}
	tests := []struct {
		name       string
		mode       PolicyMode
		policy     *policy.Policy
		host       string
		wantAction goproxy.ConnectAction
	}{
		{
			name:       "enforce rejects non-allowlisted host on non-standard port",
			mode:       EnforcementMode,
			policy:     &allowlist,
			host:       "forbidden.test:9443",
			wantAction: *goproxy.RejectConnect,
		},
		{
			name:       "enforce allows allowlisted host on non-standard port",
			mode:       EnforcementMode,
			policy:     &allowlist,
			host:       "allowed.test:9443",
			wantAction: *goproxy.OkConnect,
		},
		{
			name:       "disabled tunnels any host",
			mode:       DisabledMode,
			policy:     &allowlist,
			host:       "forbidden.test:9443",
			wantAction: *goproxy.OkConnect,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := TransparentProxyService{
				Mode:       tc.mode,
				Policy:     tc.policy,
				mx:         new(sync.Mutex),
				networkLog: &netlog.NetworkActivityLog{HTTPRequests: []netlog.HTTPRequestLog{}},
				logNetwork: true,
			}
			gotAction, gotHost := svc.HandleUninterceptedConnect(tc.host, nil)
			if gotAction == nil || gotAction.Action != tc.wantAction.Action {
				t.Errorf("HandleUninterceptedConnect(%q) action = %v, want %v", tc.host, gotAction, tc.wantAction.Action)
			}
			if gotHost != tc.host {
				t.Errorf("HandleUninterceptedConnect(%q) host = %q, want %q", tc.host, gotHost, tc.host)
			}
			// The connection must be recorded regardless of the policy decision,
			// so the NetworkRebuild network log is complete.
			reqs := svc.networkLog.HTTPRequests
			if len(reqs) != 1 {
				t.Fatalf("network log has %d entries, want 1", len(reqs))
			}
			if reqs[0].Method != http.MethodConnect || reqs[0].Host != tc.host {
				t.Errorf("network log entry = {Method:%q Host:%q}, want {Method:%q Host:%q}", reqs[0].Method, reqs[0].Host, http.MethodConnect, tc.host)
			}
		})
	}
}

// TestHandleUninterceptedConnectSkipLogging verifies that when logging is
// disabled no network-log entry is recorded, while policy is still enforced.
func TestHandleUninterceptedConnectSkipLogging(t *testing.T) {
	svc := TransparentProxyService{
		Mode: EnforcementMode,
		Policy: &policy.Policy{AnyOf: []policy.Rule{
			policy.URLMatchRule{Host: "allowed.test", HostMatch: policy.FullMatch, Path: "/", PathMatch: policy.PrefixMatch},
		}},
		mx:         new(sync.Mutex),
		networkLog: &netlog.NetworkActivityLog{HTTPRequests: []netlog.HTTPRequestLog{}},
		logNetwork: false,
	}
	action, _ := svc.HandleUninterceptedConnect("forbidden.test:9443", nil)
	if action == nil || action.Action != goproxy.RejectConnect.Action {
		t.Errorf("action = %v, want reject", action)
	}
	if got := len(svc.networkLog.HTTPRequests); got != 0 {
		t.Errorf("network log has %d entries, want 0 when logging disabled", got)
	}
}
