// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"google.golang.org/genai"
)

func TestIsTransient(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"Nil", nil, false},
		{"ContextCanceled", context.Canceled, false},
		{"WrappedContextDeadline", errors.Wrap(context.DeadlineExceeded, "sending message"), false},
		{"Code429", genai.APIError{Code: 429}, true},
		{"Code503", genai.APIError{Code: 503}, true},
		{"Code400", genai.APIError{Code: 400}, false},
		{"ResourceExhaustedStatus", genai.APIError{Status: "RESOURCE_EXHAUSTED"}, true},
		{"Wrapped429", errors.Wrap(genai.APIError{Code: 429}, "sending message"), true},
		{"RateLimitText", errors.New("upstream returned rate limit exceeded"), true},
		{"OverloadedText", errors.New("model is overloaded, try again later"), true},
		{"InvalidArgumentText", errors.New("invalid argument: bad prompt"), false},
		// Status texts come from net/http, so they track retryableCodes.
		{"DerivedStatusText", errors.New("upstream returned Service Unavailable"), true},
		{"DerivedTimeoutText", errors.New("gateway timeout while streaming"), true},
		// NOTE: Bare status numbers go unmatched on purpose. See retry.go.
		{"BareStatusNumberInProse", errors.New("request consumed 500 tokens"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
