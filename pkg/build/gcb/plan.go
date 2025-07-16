// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"google.golang.org/api/cloudbuild/v1"
)

// Plan represents a Google Cloud Build execution plan
type Plan struct {
	// Steps contains the Cloud Build steps to execute
	Steps []*cloudbuild.BuildStep
	// Dockerfile contains the generated Dockerfile content
	Dockerfile string
}
