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
		strategy    MuddleStrategy
		target      Target
		buildEnv    BuildEnv
		want        Instructions
		wantErr     bool
		errContains string
	}{
		{
			name: "empty_strategy",
			strategy: MuddleStrategy{
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
			strategy: MuddleStrategy{
				Source: []MuddleStep{{Runs: "echo source"}},
				Deps:   []MuddleStep{{Runs: "echo deps"}},
				Build:  []MuddleStep{{Runs: "echo build"}},
			},
			want: Instructions{
				Source: "echo source",
				Deps:   "echo deps",
				Build:  "echo build",
			},
		},
		{
			name: "invalid_step_both_runs_and_uses",
			strategy: MuddleStrategy{
				Source: []MuddleStep{{
					Runs: "echo test",
					Uses: "git-checkout",
				}},
			},
			wantErr:     true,
			errContains: "exactly one of 'runs' or 'uses' must be provided",
		},
		{
			name: "invalid_step_neither_runs_nor_uses",
			strategy: MuddleStrategy{
				Source: []MuddleStep{{}},
			},
			wantErr:     true,
			errContains: "exactly one of 'runs' or 'uses' must be provided",
		},
		{
			name: "unknown_uses_command",
			strategy: MuddleStrategy{
				Source: []MuddleStep{{
					Uses: "nonexistent-command",
				}},
			},
			wantErr:     true,
			errContains: "unknown 'uses' command: nonexistent-command",
		},
		{
			name: "system_deps_deduplication",
			strategy: MuddleStrategy{
				Location: Location{
					Repo: "https://github.com/test/repo",
					Ref:  "abc123",
				},
				SystemDeps: []string{"git", "npm", "git"},
				Source: []MuddleStep{{
					Uses: "git-checkout",
				}},
				Build: []MuddleStep{{
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
		strategy MuddleStrategy
		wantYAML string
	}{
		{
			name: "full_config",
			strategy: MuddleStrategy{
				Location: Location{
					Dir:  "test-dir",
					Repo: "https://example.com/test-repo",
					Ref:  "abc123",
				},
				Source: []MuddleStep{
					{Runs: "echo source"},
					{Uses: "git-checkout"},
				},
				Deps: []MuddleStep{
					{
						Uses: "npm/install",
						With: map[string]string{
							"npmVersion": "8.0.0",
						},
					},
				},
				Build: []MuddleStep{
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
			strategy: MuddleStrategy{
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
			var gotStrategy MuddleStrategy
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
			s := MuddleStrategy{Location: tt.location}
			c, err := s.generateForStep(MuddleStep{Uses: "git-checkout"}, Target{}, tt.buildEnv)
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
			s := MuddleStrategy{Location: tt.location}
			c, err := s.generateForStep(MuddleStep{Uses: "npm/install", With: tt.with}, Target{}, BuildEnv{})
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
		cmd1 command
		cmd2 command
		want command
	}{
		{
			name: "empty_commands",
			cmd1: command{},
			cmd2: command{},
			want: command{
				Script: "\n",
				Needs:  nil,
			},
		},
		{
			name: "merge_scripts_and_deps",
			cmd1: command{
				Script: "echo first",
				Needs:  []string{"dep1"},
			},
			cmd2: command{
				Script: "echo second",
				Needs:  []string{"dep2"},
			},
			want: command{
				Script: "echo first\necho second",
				Needs:  []string{"dep1", "dep2"},
			},
		},
		{
			name: "duplicate_deps",
			cmd1: command{
				Script: "echo first",
				Needs:  []string{"dep1", "dep2"},
			},
			cmd2: command{
				Script: "echo second",
				Needs:  []string{"dep2", "dep3"},
			},
			want: command{
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
