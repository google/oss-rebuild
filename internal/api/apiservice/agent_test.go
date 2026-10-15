// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"testing"

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
