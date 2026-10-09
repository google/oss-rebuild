// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestExecDetailsRendersTheSharedTimewarpHost(t *testing.T) {
	target := rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"}
	strategy := &rebuild.WorkflowStrategy{
		Location:  rebuild.Location{Repo: "https://github.com/example/pkg", Ref: "abc123", Dir: "."},
		Source:    []flow.Step{{Uses: "git-checkout"}},
		Build:     []flow.Step{{Uses: "npm/setup-registry", With: map[string]string{"registryTime": "2023-01-01T12:00:00Z"}}},
		OutputDir: ".",
	}
	iter := &schema.AgentIteration{Strategy: new(schema.NewStrategyOneOf(strategy))}
	for name, runner := range map[string]*ScratchRunner{"scratch": {}, "gcb": nil} {
		t.Run(name, func(t *testing.T) {
			a := NewDefaultAgent(target, &AgentDeps{ScratchRunner: runner})
			d := a.execDetails(context.Background(), iter)
			if !strings.Contains(d.Instructions.Build, "@localhost:8080") {
				t.Errorf("build = %q, want the registry on localhost:8080", d.Instructions.Build)
			}
		})
	}
}
