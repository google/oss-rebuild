// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"gopkg.in/yaml.v3"
)

func TestWorkflowStrategy_GenerateFor(t *testing.T) {
	tests := []struct {
		name        string
		strategy    WorkflowStrategy
		target      Target
		buildEnv    BuildEnv
		want        Instructions
		wantErr     bool
		errContains string
	}{
		{
			name: "empty_strategy",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
			},
			target:   Target{},
			buildEnv: BuildEnv{},
			want: Instructions{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
			},
		},
		{
			name: "simple_runs_commands",
			strategy: WorkflowStrategy{
				Source: []flow.Step{{Runs: "echo source"}},
				Deps:   []flow.Step{{Runs: "echo deps"}},
				Build:  []flow.Step{{Runs: "echo build"}},
			},
			want: Instructions{
				Source: "echo source",
				Deps:   "echo deps",
				Build:  "echo build",
			},
		},
		{
			name: "output_path",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
				OutputPath: "foo",
			},
			target:   Target{Artifact: "bar"},
			buildEnv: BuildEnv{},
			want: Instructions{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
				OutputPath: "foo",
			},
		},
		{
			name: "output_dir",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
				OutputDir: "foo",
			},
			target:   Target{Artifact: "bar"},
			buildEnv: BuildEnv{},
			want: Instructions{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
				OutputPath: "foo/bar",
			},
		},
		{
			name: "invalid_output_path_and_dir",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "main",
				},
				OutputPath: "foo/bar",
				OutputDir:  "foo",
			},
			target:      Target{Artifact: "bar"},
			buildEnv:    BuildEnv{},
			wantErr:     true,
			errContains: "only one",
		},
		{
			name: "invalid_step_both_runs_and_uses",
			strategy: WorkflowStrategy{
				Source: []flow.Step{{
					Runs: "echo test",
					Uses: "git-checkout",
				}},
			},
			wantErr:     true,
			errContains: "must provide exactly one of 'runs' or 'uses'",
		},
		{
			name: "invalid_step_neither_runs_nor_uses",
			strategy: WorkflowStrategy{
				Source: []flow.Step{{}},
			},
			wantErr:     true,
			errContains: "must provide exactly one of 'runs' or 'uses'",
		},
		{
			name: "unknown_uses_command",
			strategy: WorkflowStrategy{
				Source: []flow.Step{{
					Uses: "nonexistent-command",
				}},
			},
			wantErr:     true,
			errContains: `tool not found: "nonexistent-command"`,
		},
		{
			name: "system_deps_deduplication",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "abc123",
				},
				SystemDeps: []string{"git", "npm", "git"},
				Source: []flow.Step{{
					Uses: "git-checkout",
				}},
				Build: []flow.Step{{
					Runs:  "npm pack",
					Needs: []string{"npm"},
				}},
			},
			want: Instructions{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "abc123",
				},
				SystemDeps: []string{"git", "npm"},
				Source:     "git clone https://github.com/test/repo .\ngit checkout --force 'abc123'",
				Build:      "npm pack",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.strategy.GenerateFor(tt.target, tt.buildEnv)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want error containing %v", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("GenerateFor() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWorkflowStrategyYAML(t *testing.T) {
	tests := []struct {
		name     string
		strategy WorkflowStrategy
		wantYAML string
	}{
		{
			name: "full_config",
			strategy: WorkflowStrategy{
				Location: Location{
					Dir:  "test-dir",
					Repo: "https://example.com/test-repo",
					Ref:  "abc123",
				},
				Source: []flow.Step{
					{Runs: "echo source"},
					{Uses: "git-checkout"},
				},
				Deps: []flow.Step{
					{
						Uses: "not-real",
						With: map[string]string{
							"foo": "bar",
						},
					},
				},
				Build: []flow.Step{
					{Runs: "make build"},
				},
				SystemDeps: []string{"git", "npm"},
				OutputPath: "dist/output",
			},
			wantYAML: `
location:
    repo: https://example.com/test-repo
    ref: abc123
    dir: test-dir
src:
    - runs: echo source
    - uses: git-checkout
deps:
    - uses: not-real
      with:
        foo: bar
build:
    - runs: make build
system_deps:
    - git
    - npm
output_path: dist/output
`,
		},
		{
			name: "minimal_config",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://example.com/test-repo",
					Ref:  "abc123",
				},
			},
			wantYAML: `
location:
    repo: https://example.com/test-repo
    ref: abc123
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal the struct to YAML
			gotYAML, err := yaml.Marshal(tc.strategy)
			if err != nil {
				t.Fatalf("yaml.Marshal() error = %v", err)
			}

			// Compare generated YAML with expected YAML (normalizing whitespace)
			if diff := cmp.Diff(strings.TrimSpace(tc.wantYAML), strings.TrimSpace(string(gotYAML))); diff != "" {
				t.Errorf("YAML mismatch (-want +got):\n%s", diff)
			}

			// Unmarshal back to struct
			var gotStrategy WorkflowStrategy
			if err := yaml.Unmarshal(gotYAML, &gotStrategy); err != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", err)
			}

			// Compare original struct with round-tripped struct
			if diff := cmp.Diff(tc.strategy, gotStrategy); diff != "" {
				t.Errorf("Round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuiltinCommand_GitCheckout(t *testing.T) {
	tests := []struct {
		name     string
		location Location
		buildEnv BuildEnv
		want     string
	}{
		{
			name: "fresh_checkout",
			location: Location{
				Repo: "https://github.com/test/repo",
				Ref:  "deadbeef",
			},
			buildEnv: BuildEnv{HasRepo: false},
			want:     "git clone https://github.com/test/repo .\ngit checkout --force 'deadbeef'",
		},
		{
			name: "existing_repo",
			location: Location{
				Repo: "https://github.com/test/repo",
				Ref:  "0000",
			},
			buildEnv: BuildEnv{HasRepo: true},
			want:     "git checkout --force '0000'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := flow.Step{Uses: "git-checkout"}.Resolve(nil, flow.Data{"Location": tt.location, "BuildEnv": tt.buildEnv})
			if err != nil {
				t.Fatalf("Step.Resolve failed: %v", err)
			}

			if diff := cmp.Diff(tt.want, c.Script); diff != "" {
				t.Errorf("script mismatch (-want +got):\n%s", diff)
			}
			wantNeeds := []string{"git"}
			if diff := cmp.Diff(wantNeeds, c.Needs); diff != "" {
				t.Errorf("needs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
