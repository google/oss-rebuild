package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/oss-rebuild/pkg/proxy/policy"
)

func TestCheckNetworkPolicy(t *testing.T) {
	proxyService := TransparentProxyService{}
	tests := []struct {
		name     string
		mode     ProxyMode
		policy   policy.Policy
		url      string
		wantResp int
	}{
		{
			name: "empty host policy rule does not match any url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "",
						Path: "path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "partial host policy rule does not match any url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host",
						Path: "path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "path prefix matching strategy allows matching url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/path",
			wantResp: http.StatusOK,
		},
		{
			name: "path prefix matching strategy disallows non-matching url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/non/matching/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "full path matching strategy allows matching url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/matching/path",
						Type: policy.FullPath,
					},
				},
			},
			url:      "https://host.com/matching/path",
			wantResp: http.StatusOK,
		},
		{
			name: "full path matching strategy disallows non-matching url",
			mode: EnforcementMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: policy.FullPath,
					},
				},
			},
			url:      "https://host.com/path/that/does/not/match",
			wantResp: http.StatusForbidden,
		},
		{
			name: "monitor mode:empty host policy rule gets passed through",
			mode: MonitorMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "",
						Path: "path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "monitor mode:allowed url is passed through",
			mode: MonitorMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/path",
			wantResp: http.StatusOK,
		},
		{
			name: "monitor mode:disallowed url is passed through",
			mode: MonitorMode,
			policy: policy.Policy{
				Rules: []policy.Rule{
					policy.URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: policy.PathPrefix,
					},
				},
			},
			url:      "https://host.com/non/matching/path",
			wantResp: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxyService.Mode = tc.mode
			proxyService.Policy = &tc.policy
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)

			_, gotResp := proxyService.CheckNetworkPolicy(req, nil)
			// nil response means request allowed through proxy. Assume 200 status.
			if gotResp == nil && tc.wantResp != http.StatusOK {
				t.Errorf("CheckNetworkPolicy returned an unexpected response code %v, want %v", http.StatusOK, tc.wantResp)
			}
			if gotResp != nil && tc.wantResp != gotResp.StatusCode {
				t.Errorf("CheckNetworkPolicy returned an unexpected response code %v, want %v", gotResp.StatusCode, tc.wantResp)
			}
		})
	}
}
