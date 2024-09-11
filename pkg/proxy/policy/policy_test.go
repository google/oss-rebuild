package policy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnforcePolicyOnURLMatchRule(t *testing.T) {
	tests := []struct {
		name     string
		policy   Policy
		url      string
		wantResp int
	}{
		{
			name: "empty host policy rule does not match any url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "",
						Path: "path",
						Type: PathPrefix,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "partial host policy rule does not match any url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "host",
						Path: "path",
						Type: PathPrefix,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "path prefix matching type allows matching url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: PathPrefix,
					},
				},
			},
			url:      "https://host.com/path",
			wantResp: http.StatusOK,
		},
		{
			name: "path prefix matching type disallows non-matching url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: PathPrefix,
					},
				},
			},
			url:      "https://host.com/non/matching/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "full path matching type allows matching url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "host.com",
						Path: "/matching/path",
						Type: FullPath,
					},
				},
			},
			url:      "https://host.com/matching/path",
			wantResp: http.StatusOK,
		},
		{
			name: "full path matching type disallows non-matching url",
			policy: Policy{
				Rules: []Rule{
					URLMatchRule{
						Host: "host.com",
						Path: "/path",
						Type: FullPath,
					},
				},
			},
			url:      "https://host.com/path/that/does/not/match",
			wantResp: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)

			_, gotResp := tc.policy.EnforcePolicy(req, nil)
			// nil response means request allowed through proxy. Assume 200 status.
			if gotResp == nil && tc.wantResp != http.StatusOK {
				t.Errorf("EnforcePolicy returned an unexpected response code %v, want %v", http.StatusOK, tc.wantResp)
			}
			if gotResp != nil && tc.wantResp != gotResp.StatusCode {
				t.Errorf("EnforcePolicy returned an unexpected response code %v, want %v", gotResp.StatusCode, tc.wantResp)
			}
		})
	}
}
