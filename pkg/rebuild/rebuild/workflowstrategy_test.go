package rebuild

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

func TestMuddleStrategy_GenerateFor(t *testing.T) {
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
				Source: []WorkflowStep{{Runs: "echo source"}},
				Deps:   []WorkflowStep{{Runs: "echo deps"}},
				Build:  []WorkflowStep{{Runs: "echo build"}},
			},
			want: Instructions{
				Source: "echo source",
				Deps:   "echo deps",
				Build:  "echo build",
			},
		},
		{
			name: "invalid_step_both_runs_and_uses",
			strategy: WorkflowStrategy{
				Source: []WorkflowStep{{
					Runs: "echo test",
					Uses: "git-checkout",
				}},
			},
			wantErr:     true,
			errContains: "exactly one of 'runs' or 'uses' must be provided",
		},
		{
			name: "invalid_step_neither_runs_nor_uses",
			strategy: WorkflowStrategy{
				Source: []WorkflowStep{{}},
			},
			wantErr:     true,
			errContains: "exactly one of 'runs' or 'uses' must be provided",
		},
		{
			name: "unknown_uses_command",
			strategy: WorkflowStrategy{
				Source: []WorkflowStep{{
					Uses: "nonexistent-command",
				}},
			},
			wantErr:     true,
			errContains: "unknown 'uses' tool: nonexistent-command",
		},
		{
			name: "system_deps_deduplication",
			strategy: WorkflowStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "abc123",
				},
				SystemDeps: []string{"git", "npm", "git"},
				Source: []WorkflowStep{{
					Uses: "git-checkout",
				}},
				Build: []WorkflowStep{{
					Uses: "npm/install",
					With: map[string]string{"npmVersion": "8"},
				}},
			},
			want: Instructions{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "abc123",
				},
				SystemDeps: []string{"git", "npm"},
				Source:     "git clone https://github.com/test/repo .\ngit checkout --force 'abc123'",
				Build:      "PATH=/usr/local/bin:/usr/bin npx --package=npm@8 -c 'npm install --force'",
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

func TestMuddleStrategyYAML(t *testing.T) {
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
				Source: []WorkflowStep{
					{Runs: "echo source"},
					{Uses: "git-checkout"},
				},
				Deps: []WorkflowStep{
					{
						Uses: "npm/install",
						With: map[string]string{
							"npmVersion": "8.0.0",
						},
					},
				},
				Build: []WorkflowStep{
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
    - uses: npm/install
      with:
        npmVersion: 8.0.0
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
			s := WorkflowStrategy{Location: tt.location}
			c, err := s.generateForStep(WorkflowStep{Uses: "git-checkout"}, Target{}, tt.buildEnv)
			if err != nil {
				t.Fatalf("generateForStep failed: %v", err)
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

func TestBuiltinCommand_NpmInstall(t *testing.T) {
	tests := []struct {
		name     string
		location Location
		with     map[string]string
		want     string
	}{
		{
			name: "root_directory",
			location: Location{
				Dir: ".",
			},
			with: map[string]string{
				"npmVersion": "8.0.0",
			},
			want: "PATH=/usr/local/bin:/usr/bin npx --package=npm@8.0.0 -c 'npm install --force'",
		},
		{
			name: "subdirectory",
			location: Location{
				Dir: "frontend",
			},
			with: map[string]string{
				"npmVersion": "7.0.0",
			},
			want: "PATH=/usr/local/bin:/usr/bin npx --package=npm@7.0.0 -c 'cd frontend && npm install --force'",
		},
		{
			name: "missing_npm_version",
			location: Location{
				Dir: "frontend",
			},
			with: map[string]string{},
			want: "PATH=/usr/local/bin:/usr/bin npx --package=npm -c 'cd frontend && npm install --force'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := WorkflowStrategy{Location: tt.location}
			c, err := s.generateForStep(WorkflowStep{Uses: "npm/install", With: tt.with}, Target{}, BuildEnv{})
			if err != nil {
				t.Fatalf("generateForStep failed: %v", err)
			}

			if diff := cmp.Diff(tt.want, c.Script); diff != "" {
				t.Errorf("script mismatch (-want +got):\n%s", diff)
			}
			wantNeeds := []string{"npm"}
			if diff := cmp.Diff(wantNeeds, c.Needs); diff != "" {
				t.Errorf("needs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCommand_Join(t *testing.T) {
	tests := []struct {
		name string
		cmd1 task
		cmd2 task
		want task
	}{
		{
			name: "empty_commands",
			cmd1: task{},
			cmd2: task{},
			want: task{
				Script: "\n",
				Needs:  nil,
			},
		},
		{
			name: "merge_scripts_and_deps",
			cmd1: task{
				Script: "echo first",
				Needs:  []string{"dep1"},
			},
			cmd2: task{
				Script: "echo second",
				Needs:  []string{"dep2"},
			},
			want: task{
				Script: "echo first\necho second",
				Needs:  []string{"dep1", "dep2"},
			},
		},
		{
			name: "duplicate_deps",
			cmd1: task{
				Script: "echo first",
				Needs:  []string{"dep1", "dep2"},
			},
			cmd2: task{
				Script: "echo second",
				Needs:  []string{"dep2", "dep3"},
			},
			want: task{
				Script: "echo first\necho second",
				Needs:  []string{"dep1", "dep2", "dep2", "dep3"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cmd1.Join(tt.cmd2)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Join() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
