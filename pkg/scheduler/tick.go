// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "time"

// Tick advances a campaign given the outcome of its last attempt. Attested
// finishes it. Transient re-queues the same stage, and goes to NeedsTriage
// after maxRetries (unbounded when <= 0) since a wedged stage looks the same.
// Failure escalates to the next stage, or to NeedsTriage when none is left.
// Oversize re-queues as Jumbo, or goes to NeedsTriage when already Jumbo.
// GaveUp goes to NeedsTriage with the agent's closest build.
func Tick(c Campaign, outcome Outcome, maxRetries int, now time.Time) Campaign {
	c.Outcome = outcome
	c.Updated = now
	switch outcome {
	case OutcomeAttested:
		c.State = StateDone
	case OutcomeTransient:
		c.Retries++
		if maxRetries > 0 && c.Retries >= maxRetries {
			c.State = StateNeedsTriage
			c.TriageReason = "persistent transient failures"
			return c
		}
		c.State = StateQueued
	case OutcomeFailure:
		if next, ok := c.Stage.Next(); ok {
			c.Stage = next
			c.State = StateQueued
			c.Retries = 0
		} else {
			c.State = StateNeedsTriage
			c.TriageReason = "agent stage exhausted"
		}
	case OutcomeOversize:
		if !c.Jumbo {
			c.Jumbo = true
			c.State = StateQueued
			return c
		}
		c.State = StateNeedsTriage
		c.TriageReason = "repository exceeds size limit"
	case OutcomeGaveUp:
		c.State = StateNeedsTriage
		c.TriageReason = "agent gave up with its closest build"
	}
	return c
}
