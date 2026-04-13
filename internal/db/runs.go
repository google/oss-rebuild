// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func runPath(r schema.Run) []string { return []string{"runs", r.ID} }
func runKey(id string) []string     { return []string{"runs", id} }

func NewFirestoreRuns(c *firestore.Client) Runs {
	return &firestoreResource[schema.Run, string]{client: c, pathFor: runPath, pathForKey: runKey}
}

func NewMemoryRuns() Runs {
	return &memoryResource[schema.Run, string]{data: map[string]schema.Run{}, pathFor: runPath, pathForKey: runKey}
}
