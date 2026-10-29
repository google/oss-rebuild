// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestScratchLabelNames(t *testing.T) {
	for _, tc := range []struct {
		name      string
		label     ScratchLabel
		wantPhase rebuild.BuildPhase
		wantTool  string
	}{
		{"Phase", PhaseLabel(rebuild.PhaseDeps), rebuild.PhaseDeps, ""},
		{"Tool", ToolLabel("run_in_container"), "", "run_in_container"},
		{"BarePhase", PhaseLabel(""), "", ""},
		{"BareTool", ToolLabel(""), "", ""},
		{"Machinery", ScratchLabelKeepAlive, "", ""},
		{"Unlabeled", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if phase, ok := tc.label.Phase(); phase != tc.wantPhase || ok != (tc.wantPhase != "") {
				t.Errorf("Phase() = %q, %v, want %q, %v", phase, ok, tc.wantPhase, tc.wantPhase != "")
			}
			if tool, ok := tc.label.Tool(); tool != tc.wantTool || ok != (tc.wantTool != "") {
				t.Errorf("Tool() = %q, %v, want %q, %v", tool, ok, tc.wantTool, tc.wantTool != "")
			}
		})
	}
}
