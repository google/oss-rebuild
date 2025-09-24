// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rebuild contains interfaces implementing generic rebuild functionality.
package rebuild

type ctxKey int

const (
	RetainArtifactsID ctxKey = iota
	AssetDirID
	DebugStoreID
	RepoCacheClientID
	HTTPBasicClientID
	InvocationID
	TimewarpID
	RunID
	GCSClientOptionsID
	GCBCancelDeadlineID
	GCBWaitDeadlineID
	CratesRegistryStubID
)
