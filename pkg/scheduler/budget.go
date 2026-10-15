// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"net/url"
	"time"
)

// Budget is the spend ledger for one scope over one period. Caps live in the
// dispatch config, so the ledger records only what was spent and since when.
// Tokens are recorded but not capped.
type Budget struct {
	Scope          string    `firestore:"scope,omitempty"`
	PeriodStart    time.Time `firestore:"period_start,omitempty"`
	BuildSeconds   int64     `firestore:"build_seconds,omitempty"`
	SessionSeconds int64     `firestore:"session_seconds,omitempty"`
	Tokens         int64     `firestore:"tokens,omitempty"`
	Updated        time.Time `firestore:"updated,omitempty"`
}

// Spend is one completion's resource usage.
type Spend struct {
	BuildSeconds   int64
	SessionSeconds int64
	Tokens         int64
}

// Add accumulates one completion's spend onto the ledger.
func (b *Budget) Add(s Spend) {
	b.BuildSeconds += s.BuildSeconds
	b.SessionSeconds += s.SessionSeconds
	b.Tokens += s.Tokens
}

// BudgetScopeGlobal is the ledger scope covering all spend.
const BudgetScopeGlobal = "global"

// BudgetScopeEcosystem is the ledger scope covering one ecosystem's spend.
func BudgetScopeEcosystem(ecosystem string) string {
	return "eco!" + url.PathEscape(ecosystem)
}

// Expired reports whether the period beginning at PeriodStart has elapsed.
// period <= 0 never expires.
func (b Budget) Expired(now time.Time, period time.Duration) bool {
	return period > 0 && !b.PeriodStart.IsZero() && now.Sub(b.PeriodStart) > period
}
