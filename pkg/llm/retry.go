// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// retryableCodes are the HTTP status codes worth retrying.
var retryableCodes = map[int]bool{
	http.StatusRequestTimeout:      true,
	http.StatusTooManyRequests:     true, // quota and rate limits
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true, // also how overload is reported
	http.StatusGatewayTimeout:      true,
}

// retryableStatuses are the gRPC status names backends set in place of a code.
var retryableStatuses = []string{"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "ABORTED"}

// providerPhrases is transient vocabulary with no HTTP status text behind it.
var providerPhrases = []string{
	"rate limit", "resource exhausted", "resource_exhausted",
	"unavailable", "quota", "overloaded", "try again",
	"deadline exceeded", "connection reset", "temporarily",
}

// transientPhrases match errors whose type is no longer inspectable but whose
// message still names a transient condition from retryableCodes.
var transientPhrases = func() []string {
	out := slices.Clone(providerPhrases)
	for code := range retryableCodes {
		out = append(out, strings.ToLower(http.StatusText(code)))
	}
	return out
}()

// IsTransient reports whether err is provider throttling or a temporary server
// error, as opposed to a bad request or a model refusal.
// NOTE: Prefer false positives here to bias failures towards retries rather
// than giving up on transient failures.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// Caller cancellation is not a provider condition and must not be retried,
	// even though "deadline exceeded" appears in the transient vocabulary.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		if retryableCodes[apiErr.Code] {
			return true
		}
		if slices.Contains(retryableStatuses, strings.ToUpper(apiErr.Status)) {
			return true
		}
	}
	// Fall back to matching the message for wrapped or opaque errors.
	msg := strings.ToLower(err.Error())
	return slices.ContainsFunc(transientPhrases, func(p string) bool { return strings.Contains(msg, p) })
}
