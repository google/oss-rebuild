// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"time"

	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/llm"
)

const (
	llmMinInterval = 1 * time.Second // base retry interval, grows with each retriable failure
	llmMaxInterval = 2 * time.Minute // ceiling on the interval however long throttling lasts
	llmAttempts    = 12              // total will exceed 60s, the Vertex quota window
)

// NewRetrier returns the default model-call retrier for one agent process.
func NewRetrier() ratex.Retrier {
	return ratex.Retrier{
		Limiter:   ratex.NewBackoffLimiter(llmMinInterval, llmMaxInterval),
		Attempts:  llmAttempts,
		Retryable: llm.IsTransient,
	}
}
