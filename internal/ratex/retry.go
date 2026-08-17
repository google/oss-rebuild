// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ratex

import (
	"context"

	"github.com/pkg/errors"
)

// Retrier retries work under a BackoffLimiter.
type Retrier struct {
	Limiter   *BackoffLimiter  // how long to wait when retrying
	Attempts  int              // max attempts, <=1 meaning no retries
	Retryable func(error) bool // whether an error should be retried, nil meaning all are
}

// Do calls fn until it succeeds, returns an error not configured to retry, or
// runs out of attempts. The last fn invocation's error is returned unwrapped.
// A context cancellation during wait is returned wrapped.
func (r Retrier) Do(ctx context.Context, fn func() error) error {
	if r.Limiter == nil { // no limiter configured
		return fn()
	}
	attempts := max(r.Attempts, 1)
	var err error
	for range attempts {
		if werr := r.Limiter.Wait(ctx); werr != nil {
			return errors.Wrap(werr, "awaiting rate limiter")
		}
		if err = fn(); err == nil {
			r.Limiter.Success()
			return nil
		}
		if r.Retryable != nil && !r.Retryable(err) {
			return err
		}
		r.Limiter.Backoff()
	}
	return err
}
