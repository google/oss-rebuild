// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import "fmt"

// RepoTooLargeError reports a fetch cut off at the caller's size budget.
type RepoTooLargeError struct {
	URL   string
	Limit int64
}

func (e *RepoTooLargeError) Error() string {
	return fmt.Sprintf("repository exceeds the size limit: %s, fetch cut off at %d MiB", e.URL, e.Limit>>20)
}
