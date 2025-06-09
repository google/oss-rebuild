// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// ToolType represents types of tools used during builds
type ToolType string

const (
	TimewarpTool ToolType = "timewarp"
	ProxyTool    ToolType = "proxy"
	GSUtilTool   ToolType = "gsutil_writeonly"
)

// Executor manages build execution for a specific backend
type Executor interface {
	Start(ctx context.Context, input rebuild.Input, opts Options) (Handle, error)
	Status() ExecutorStatus
	Close(ctx context.Context) error
}

// Handle represents an active or completed build
type Handle interface {
	BuildID() string
	Wait(ctx context.Context) (Result, error)
	OutputStream() io.Reader
	Status() BuildState
}

// Result represents the completed build result
type Result struct {
	// Error represents a build-time failure (i.e. after build setup)
	Error error
	// Timings describes execution duration of various aspects of the build
	Timings rebuild.Timings
}

// ExecutorStatus represents the overall executor status
type ExecutorStatus struct {
	// InProgress is the number of builds currently executing
	InProgress int
	// Capacity is the max number of builds that can be exected simultanously
	Capacity int
	// Healthy is whether the executor is accepting new builds
	Healthy bool
}

// BuildState represents the current state of a build
type BuildState int

const (
	BuildStateStarting BuildState = iota
	BuildStateRunning
	BuildStateCompleted
	BuildStateCancelled
)

func (s BuildState) String() string {
	switch s {
	case BuildStateStarting:
		return "starting"
	case BuildStateRunning:
		return "running"
	case BuildStateCompleted:
		return "completed"
	case BuildStateCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}
