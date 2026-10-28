// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import "fmt"

// RepoTooLargeError reports a repository over the caller's size budget.
type RepoTooLargeError struct {
	URL   string
	Limit int64
	Size  int64 // declared archive size, or 0
}

func (e *RepoTooLargeError) Error() string {
	if e.Size > 0 {
		return fmt.Sprintf("repository exceeds the size limit: %s, archive is %d MiB, limit %d MiB", e.URL, e.Size>>20, e.Limit>>20)
	}
	return fmt.Sprintf("repository exceeds the size limit: %s, fetch cut off at %d MiB", e.URL, e.Limit>>20)
}
