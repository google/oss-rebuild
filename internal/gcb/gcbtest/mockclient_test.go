// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcbtest

import (
	"testing"

	"github.com/google/oss-rebuild/internal/gcb"
)

func TestBuild(t *testing.T) {
	var _ gcb.Client = &MockClient{}
}
