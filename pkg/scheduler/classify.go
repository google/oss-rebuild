// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"reflect"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// transientBuildSignatures are message fragments from infrastructure failures
// that say nothing about the package. Kept small until a failure taxonomy
// exists. The gateway service answers only 302 or 400, and its client reports
// 400 separately, so any other "requesting gateway:" status is serving
// infrastructure. The inference call's 504 is excluded: it recurs for repos
// whose inference exceeds the deadline.
var transientBuildSignatures = []string{
	"connection reset by peer",
	"connection to gateway failed",
	"crates.io connection failed",
	"context deadline exceeded",
	"gcb build internal error",
	"i/o timeout",
	"requesting gateway:",
	"too many requests",
	"try again",
}

// cachedSignatures mark an attempt that found an attestation already published.
var cachedSignatures = []string{"alreadyexists", "already exists"}

// ClassifyRebuild maps a rebuild attempt's status and message onto an
// Outcome. Successes and attempts that found an existing attestation are
// Attested. Cancellations and known infrastructure failures are Transient.
// Everything else, notably a content mismatch, is a Failure. Attempts that
// predate the status field carry only a message, so an unspecified status
// with an empty message is Attested.
func ClassifyRebuild(status schema.RebuildStatus, message string) Outcome {
	msg := strings.ToLower(message)
	for _, s := range cachedSignatures {
		if strings.Contains(msg, s) {
			return OutcomeAttested
		}
	}
	switch status {
	case schema.RebuildStatusSuccess:
		return OutcomeAttested
	case schema.RebuildStatusCancelled:
		return OutcomeTransient
	case schema.RebuildStatusUnspecified:
		if message == "" {
			return OutcomeAttested
		}
	}
	for _, s := range transientBuildSignatures {
		if strings.Contains(msg, s) {
			return OutcomeTransient
		}
	}
	return OutcomeFailure
}

// ClassifySession maps an agent session's stop reason onto an Outcome. Only
// SUCCESS, which the agent reports after a confirmed build, is Attested.
// THROTTLED is Transient. Anything else is a Failure.
func ClassifySession(stopReason string) Outcome {
	switch stopReason {
	case schema.AgentCompleteReasonSuccess:
		return OutcomeAttested
	case schema.AgentCompleteReasonThrottled:
		return OutcomeTransient
	default:
		return OutcomeFailure
	}
}

// IsTerminal reports whether a rebuild status is final.
func IsTerminal(status schema.RebuildStatus) bool {
	switch status {
	case schema.RebuildStatusSuccess, schema.RebuildStatusFailure,
		schema.RebuildStatusError, schema.RebuildStatusCancelled:
		return true
	default:
		return false
	}
}

// DefaultJumboRepoBytes is the RepoMetrics.Bytes value at or above which a
// package's builds are routed to the jumbo pool.
const DefaultJumboRepoBytes int64 = 500 * 1024 * 1024 // 500 MiB

// SizeHintForBytes picks a build pool from a repo's measured size. threshold<=0
// uses DefaultJumboRepoBytes. bytes<=0 (unknown size) yields the small pool.
func SizeHintForBytes(bytes, threshold int64) schema.SizeHint {
	if threshold <= 0 {
		threshold = DefaultJumboRepoBytes
	}
	if bytes >= threshold {
		return schema.JumboSize
	}
	return schema.ShrimpSize
}

// RepoFromStrategy returns the repository URL from a strategy's embedded
// Location, or "" when it has none. Dispatch uses it to route later stages
// by RepoMetrics.
func RepoFromStrategy(oneof schema.StrategyOneOf) string {
	s, err := oneof.Strategy()
	if err != nil || s == nil {
		return ""
	}
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName("Location")
	if !f.IsValid() || !f.CanInterface() {
		return ""
	}
	if loc, ok := f.Interface().(rebuild.Location); ok {
		return loc.Repo
	}
	return ""
}
