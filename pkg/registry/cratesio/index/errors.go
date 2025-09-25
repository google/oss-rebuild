// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"fmt"
	"time"
)

// RegistryOutOfDateError indicates that the current registry did not contain an update after the requested time.
type RegistryOutOfDateError struct {
	// ConstraintTime is the time that was required to be contained within the repository
	ConstraintTime time.Time
	// HeadCommitTime is the current repository's head commit time
	HeadCommitTime time.Time
	// NextUpdateTime is when the repository is scheduled for next update
	NextUpdateTime time.Time
	// UpdateInterval is the configured update interval
	UpdateInterval time.Duration
}

func (e *RegistryOutOfDateError) Error() string {
	return fmt.Sprintf("registry constraint not satisfied: requested time %v is after latest commit %v",
		e.ConstraintTime.Format(time.RFC3339), e.HeadCommitTime.Format(time.RFC3339))
}
