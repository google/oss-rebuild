package policy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplyOnURLMatchRule(t *testing.T) {
	tests := []struct {
		name     string
		policy   Policy
		url      string
		wantResp int
	}{
		{
			name: "nil policy blocks everything",
			policy: Policy{
				AnyOf: nil,
			},
			url:      "https://host.com/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "empty policy blocks everything",
			policy: Policy{
				AnyOf: []Rule{},
			},
			url:      "https://host.com/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "empty HostMatch blocks everything",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "",
						Path:      "",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "empty host and path allows all through prefix match",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "",
						HostMatch: PrefixMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "empty host policy rule does not fully match url host",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "",
						HostMatch: FullMatch,
						Path:      "path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "partial host policy rule does not fully match url host",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host",
						HostMatch: FullMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "partial host policy rule matches partially with url host",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host",
						HostMatch: PrefixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "path prefix matching type allows matching url",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path",
			wantResp: http.StatusOK,
		},
		{
			name: "path prefix matching type disallows non-matching url",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/non/matching/path",
			wantResp: http.StatusForbidden,
		},
		{
			name: "full path matching type allows matching url",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "/matching/path",
						PathMatch: FullMatch,
					},
				},
			},
			url:      "https://host.com/matching/path",
			wantResp: http.StatusOK,
		},
		{
			name: "full path matching type disallows non-matching url",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "/path",
						PathMatch: FullMatch,
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

			_, gotResp := tc.policy.Apply(req, nil)
			// nil response means request allowed through proxy. Assume 200 status.
			if gotResp == nil && tc.wantResp != http.StatusOK {
				t.Errorf("Apply returned an unexpected response code %v, want %v", http.StatusOK, tc.wantResp)
			}
			if gotResp != nil && tc.wantResp != gotResp.StatusCode {
				t.Errorf("Apply returned an unexpected response code %v, want %v", gotResp.StatusCode, tc.wantResp)
			}
		})
	}
}
