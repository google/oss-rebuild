// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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
			name: "empty host and path allows all through suffix match",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "",
						HostMatch: SuffixMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path",
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
			name: "host policy rule does not fully match url host",
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
			name: "host policy rule matches full url host by suffix",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: SuffixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "host policy rule matches url domain suffix",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: SuffixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://sub.host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "host policy rule allows matching tld suffix",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      ".com",
						HostMatch: SuffixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusOK,
		},
		{
			name: "host policy rule blocks non matching domain suffix",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "host",
						HostMatch: SuffixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://nothost.com/path/with/prefix",
			wantResp: http.StatusForbidden,
		},
		{
			name: "host policy rule blocks non matching tld suffix",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      ".net",
						HostMatch: SuffixMatch,
						Path:      "/path",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://host.com/path/with/prefix",
			wantResp: http.StatusForbidden,
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
		{
			name: "url satisfies AllOf Rules",
			policy: Policy{
				AllOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "",
						PathMatch: FullMatch,
					},
				},
			},
			url:      "https://host.com",
			wantResp: http.StatusOK,
		},
		{
			name: "url blocked by AllOf Rules",
			policy: Policy{
				AllOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "",
						PathMatch: FullMatch,
					},
				},
			},
			url:      "https://host.com/path/not/allowed",
			wantResp: http.StatusForbidden,
		},
		{
			name: "AllOf prioritized over AnyOf",
			policy: Policy{
				AnyOf: []Rule{
					URLMatchRule{
						Host:      "",
						HostMatch: SuffixMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
				},
				AllOf: []Rule{
					URLMatchRule{
						Host:      "host.com",
						HostMatch: FullMatch,
						Path:      "",
						PathMatch: PrefixMatch,
					},
				},
			},
			url:      "https://nothost.com",
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
