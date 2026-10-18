// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestAgentCreateMachineClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class schema.MachineClass
		want  schema.MachineClass
	}{
		{"DefaultsToStandard", "", schema.MachineClassStandard},
		{"Jumbo", schema.MachineClassJumbo, schema.MachineClassJumbo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got schema.MachineClass
			deps := &AgentCreateDeps{
				Sessions: db.NewMemorySessions(),
				ScratchCreateStub: func(_ context.Context, r schema.ScratchCreateRequest) (*schema.Scratch, error) {
					got = r.MachineClass
					return &schema.Scratch{ID: "scratch-1"}, nil
				},
			}
			req := schema.AgentCreateRequest{
				Target:        rebuild.Target{Ecosystem: rebuild.NPM, Package: "p", Version: "1.0.0", Artifact: "p-1.0.0.tgz"},
				ExternalAgent: true,
				MachineClass:  tc.class,
			}
			if _, err := AgentCreate(context.Background(), req, deps); err != nil {
				t.Fatalf("AgentCreate: %v", err)
			}
			if got != tc.want {
				t.Errorf("scratch MachineClass = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentJobArgsForInference(t *testing.T) {
	deps := AgentCreateDeps{InferenceURL: "https://inf", JumboInferenceURL: "https://inf-jumbo"}
	for _, tc := range []struct {
		name  string
		class schema.MachineClass
		deps  AgentCreateDeps
		want  []string
	}{
		{name: "InfersInJob", class: schema.MachineClassStandard},
		{name: "Standard", class: schema.MachineClassStandard, deps: deps, want: []string{"--inference-url=https://inf", "--jumbo-inference-url=https://inf-jumbo"}},
		{name: "Jumbo", class: schema.MachineClassJumbo, deps: deps, want: []string{"--inference-url=https://inf", "--jumbo-inference-url=https://inf-jumbo", "--jumbo"}},
		{name: "JumboWithoutJumboService", class: schema.MachineClassJumbo, deps: AgentCreateDeps{InferenceURL: "https://inf"}, want: []string{"--inference-url=https://inf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, inferenceArgs(tc.class, &tc.deps)); diff != "" {
				t.Errorf("inferenceArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
