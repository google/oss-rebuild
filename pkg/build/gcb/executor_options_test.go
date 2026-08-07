// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"errors"
	"testing"

	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type captureSavePlanOptions struct {
	got build.PlanOptions
}

func (p *captureSavePlanOptions) GeneratePlan(_ context.Context, _ rebuild.Input, opts build.PlanOptions) (*Plan, error) {
	p.got = opts
	return nil, errors.New("stop after capture")
}

func TestExecutorPropagatesSaveOptions(t *testing.T) {
	planner := &captureSavePlanOptions{}
	e := &Executor{planner: planner}
	_, err := e.Start(context.Background(), rebuild.Input{}, build.Options{
		BuildID:                "save-options",
		SaveContainerImage:     true,
		SavePostBuildContainer: true,
	})
	if err == nil {
		t.Fatal("Start() error = nil, want capture sentinel")
	}
	if !planner.got.SaveContainerImage {
		t.Error("PlanOptions.SaveContainerImage = false, want true")
	}
	if !planner.got.SavePostBuildContainer {
		t.Error("PlanOptions.SavePostBuildContainer = false, want true")
	}
}
