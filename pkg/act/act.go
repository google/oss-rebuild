// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package act provides transport-agnostic abstractions for building actions
// that can be exposed via HTTP, CLI, or other interfaces.
package act

import "context"

// Input is a validated input type (request, config, etc.)
type Input interface {
	Validate() error
}

// Deps is a marker type for dependency containers.
type Deps any

// InitDeps initializes dependencies from context.
type InitDeps[D Deps] func(context.Context) (D, error)

// Action is a transport-agnostic operation.
type Action[I Input, O any, D Deps] func(context.Context, I, D) (*O, error)

// NoDeps is a zero-value dependency container.
type NoDeps struct{}

// NoDepsInit is an InitDeps that returns NoDeps.
func NoDepsInit(context.Context) (*NoDeps, error) { return &NoDeps{}, nil }

// NoOutput is a zero-value output for actions that only produce side effects.
type NoOutput struct{}
