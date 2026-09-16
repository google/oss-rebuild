// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "time"

// Tick advances a campaign given the outcome of its last attempt. Attested
// finishes it. Transient re-queues the same stage. Failure escalates to the
// next stage, or to NeedsTriage when none is left. A stage that has absorbed
// maxRetries transient outcomes also goes to NeedsTriage, since it is
// indistinguishable from one that is wedged. maxRetries <= 0 disables that
// bound.
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
	}
	return c
}
