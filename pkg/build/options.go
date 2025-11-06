// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Resources configures URLs and authentication for build resources
type Resources struct {
	// AssetStore provides URLs and writing capabilities for build assets
	AssetStore rebuild.LocatableAssetStore
	// ToolURLs maps tool types to their download URLs
	ToolURLs map[ToolType]string
	// ToolAuthRequired lists URL prefixes that require authentication for tools
	ToolAuthRequired []string
	// BaseImageConfig defines the selection criteria for base images
	BaseImageConfig BaseImageConfig
}

// Options configures build execution behavior
type Options struct {
	// CancelPolicy determines how cancellation is handled
	CancelPolicy CancelPolicy
	// Timeout for the build execution
	// TODO: Consider changing to Deadline
	Timeout time.Duration
	// BuildID allows specifying a custom build identifier
	BuildID string
	// UseTimewarp enables timewarp functionality for builds
	UseTimewarp bool
	// UseNetworkProxy enables network proxy functionality
	UseNetworkProxy bool
	// UseSyscallMonitor enables syscall monitoring
	UseSyscallMonitor bool
	// Resources configures URLs and authentication for build resources
	Resources Resources
}

// PlanOptions configures plan generation behavior and resources
type PlanOptions struct {
	// UseTimewarp enables timewarp functionality for builds
	UseTimewarp bool
	// UseNetworkProxy enables network proxy functionality
	UseNetworkProxy bool
	// UseSyscallMonitor enables syscall monitoring
	UseSyscallMonitor bool
	// Resources configures URLs and authentication for build resources
	Resources Resources
}

// CancelPolicy determines how build cancellation is handled
type CancelPolicy int

const (
	// CancelImmediate terminates the build immediately
	CancelImmediate CancelPolicy = iota
	// CancelGraceful allows the build to finish current step before cancelling
	CancelGraceful
	// CancelDetached allows the build to continue running but detaches the handle
	CancelDetached
)

func (p CancelPolicy) String() string {
	switch p {
	case CancelImmediate:
		return "immediate"
	case CancelGraceful:
		return "graceful"
	case CancelDetached:
		return "detached"
	default:
		return "unknown"
	}
}
