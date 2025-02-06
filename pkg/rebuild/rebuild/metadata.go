// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"time"

	"google.golang.org/api/cloudbuild/v1"
)

type BuildInfo struct {
	Target      Target
	ID          string
	Builder     string
	BuildID     string
	BuildStart  time.Time
	BuildEnd    time.Time
	BuildImages map[string]string
	Steps       []*cloudbuild.BuildStep
}
