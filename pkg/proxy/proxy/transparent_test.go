package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/non/compliant/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "MonitorMode passes compliant request through",
			mode: DisabledMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
						Path:      "/path",
						PathMatch: policy.PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/compliance",
			wantResp: http.StatusOK,
		},
		{
			name: "MonitorMode passes non-compliant request through",
			mode: DisabledMode,
			policy: policy.Policy{
				AnyOf: []policy.Rule{
					policy.URLMatchRule{
						Host:      "host.com",
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
