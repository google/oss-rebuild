// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Fuzz Testing for Implementation Equivalence
//
// This file uses Go's built-in fuzzing to verify that the legacy and new
// implementations produce equivalent results for randomly generated inputs.
// Fuzzing is superior to hand-written test cases because:
//
// 1. It tests thousands of combinations automatically
// 2. It finds edge cases you didn't think to test manually
// 3. It provides regression protection as the codebase evolves
// 4. It focuses on problematic areas like whitespace handling
//
// The core property being tested: "For any valid input, both implementations
// must produce identical Dockerfiles and Cloud Build steps"
//
// Usage Examples:
//   # Basic fuzz test (comprehensive parameters)
//   go test -fuzz FuzzImplementationEquivalence -fuzztime 30s
//
//   # Run all fuzz tests briefly
//   go test -fuzz . -fuzztime 5s
//
//   # Generate corpus and run for longer periods
//   go test -fuzz FuzzImplementationEquivalence -fuzztime 5m

package gcb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func generateNewImplementationPlan(input rebuild.Input, opts rebuild.RemoteOptions) (*Plan, error) {
	plannerConfig := PlannerConfig{
		Project:        opts.Project,
		ServiceAccount: opts.BuildServiceAccount,
		LogsBucket:     opts.LogsBucket,
	}
	planner := NewPlanner(plannerConfig)
	baseImageConfig := build.DefaultBaseImageConfig()
	toolURLs := make(map[build.ToolType]string)
	baseURL := "https://" + opts.PrebuildConfig.Bucket + ".storage.googleapis.com/"
	if opts.PrebuildConfig.Dir != "" {
		baseURL += opts.PrebuildConfig.Dir + "/"
	}
	toolURLs[build.TimewarpTool] = baseURL + "timewarp"
	toolURLs[build.ProxyTool] = baseURL + "proxy"
	toolURLs[build.GSUtilTool] = baseURL + "gsutil_writeonly"
	var toolAuthRequired []string
	if opts.PrebuildConfig.Auth {
		toolAuthRequired = []string{"https://" + opts.PrebuildConfig.Bucket + ".storage.googleapis.com/"}
	}
	planOpts := build.PlanOptions{
		UseTimewarp:            opts.UseTimewarp,
		UseNetworkProxy:        opts.UseNetworkProxy,
		UseSyscallMonitor:      opts.UseSyscallMonitor,
		PreferPreciseToolchain: true,
		Resources: build.Resources{
			AssetStore:       opts.RemoteMetadataStore,
			ToolURLs:         toolURLs,
			ToolAuthRequired: toolAuthRequired,
			BaseImageConfig:  baseImageConfig,
		},
	}
	return planner.GeneratePlan(context.Background(), input, planOpts)
}

// TestImplementationComparison tests that the new pkg/build/gcb implementation
// generates the same Dockerfiles and Cloud Build steps as the legacy implementation
func TestImplementationComparison(t *testing.T) {
	type testCase struct {
		name     string
		target   rebuild.Target
		strategy rebuild.Strategy
		opts     rebuild.RemoteOptions
	}

	// Use consistent asset store for all tests
	assetStore := rebuild.NewFilesystemAssetStore(memfs.New())

	testCases := []testCase{
		{
			name:   "Standard Build",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap"},
				RemoteMetadataStore: assetStore,
			},
		},
		{
			name:   "Build with Syscall Monitor",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap"},
				RemoteMetadataStore: assetStore,
				UseSyscallMonitor:   true,
			},
		},
		{
			name:   "Build with Auth",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap", Auth: true},
				RemoteMetadataStore: assetStore,
			},
		},
		{
			name:   "Proxy Build",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap"},
				RemoteMetadataStore: assetStore,
				UseNetworkProxy:     true,
			},
		},
		{
			name:   "Proxy Build at Subdir",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap", Dir: "v0.0.0-202501010000-feeddeadbeef00"},
				RemoteMetadataStore: assetStore,
				UseNetworkProxy:     true,
			},
		},
		{
			name:   "Proxy Build with Auth",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap", Auth: true},
				RemoteMetadataStore: assetStore,
				UseNetworkProxy:     true,
			},
		},
		{
			name:   "Build with Timewarp",
			target: rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "version", Artifact: "pkg-version.tgz"},
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
				SystemDeps: []string{"git", "make"},
				Deps:       "make deps ...",
				Build:      "make build ...",
				OutputPath: "output/foo.tgz",
			},
			opts: rebuild.RemoteOptions{
				Project:             "test-project",
				LogsBucket:          "test-logs-bucket",
				BuildServiceAccount: "projects/test-project/serviceAccounts/test-service-account@test-project.iam.gserviceaccount.com",
				PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bootstrap"},
				RemoteMetadataStore: assetStore,
				UseTimewarp:         true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create input for tests that need it
			input := rebuild.Input{
				Target:   tc.target,
				Strategy: tc.strategy,
			}
			// Test legacy implementation
			legacyDockerfile, err := rebuild.MakeDockerfile(input, tc.opts)
			if err != nil {
				t.Fatalf("Legacy MakeDockerfile failed: %v", err)
			}
			legacyBuild, err := rebuild.MakeBuild(tc.target, legacyDockerfile, tc.opts)
			if err != nil {
				t.Fatalf("Legacy MakeBuild failed: %v", err)
			}
			// Test new implementation
			plan, err := generateNewImplementationPlan(input, tc.opts)
			if err != nil {
				t.Fatalf("GeneratePlan failed: %v", err)
			}
			// Compare dockerfile and build steps (should be identical)
			if diff := cmp.Diff(legacyDockerfile, plan.Dockerfile); diff != "" {
				t.Errorf("Dockerfiles differ (-legacy +new):\n%s", diff)
			}
			if diff := cmp.Diff(legacyBuild.Steps, plan.Steps); diff != "" {
				t.Errorf("Steps diff: %s", diff)
			}
		})
	}
}

// Helper function to check if two implementations produce equivalent results
func checkEquivalence(target rebuild.Target, strategy rebuild.ManualStrategy, opts rebuild.RemoteOptions) (bool, string) {
	// Skip invalid combinations that would cause both implementations to fail
	if target.Package == "" || target.Version == "" || target.Artifact == "" {
		return true, "skipped: empty required target fields"
	}
	if strategy.OutputPath == "" || strategy.Build == "" {
		return true, "skipped: empty required strategy fields"
	}
	if opts.Project == "" || opts.LogsBucket == "" {
		return true, "skipped: empty required option fields"
	}
	input := rebuild.Input{
		Target:   target,
		Strategy: &strategy,
	}
	// Test legacy implementation
	legacyDockerfile, err1 := rebuild.MakeDockerfile(input, opts)
	if err1 != nil {
		// If legacy fails, we expect new implementation to also fail
		_, err2 := generateNewImplementationPlan(input, opts)
		if err2 != nil {
			return true, "skipped: both implementations failed"
		}
		return false, fmt.Sprintf("legacy failed (%v) but new succeeded", err1)
	}
	legacyBuild, err1 := rebuild.MakeBuild(target, legacyDockerfile, opts)
	if err1 != nil {
		return true, "skipped: legacy MakeBuild failed"
	}
	// Test new implementation
	plan, err2 := generateNewImplementationPlan(input, opts)
	if err2 != nil {
		return false, fmt.Sprintf("new implementation failed (%v) when legacy succeeded", err2)
	}
	newDockerfile := plan.Dockerfile
	newBuildSteps := plan.Steps
	// Compare results
	dockerfilesEqual := legacyDockerfile == plan.Dockerfile
	stepsEqual := cmp.Equal(legacyBuild.Steps, plan.Steps)
	if !dockerfilesEqual || !stepsEqual {
		var diffDetails strings.Builder
		if !dockerfilesEqual {
			diffDetails.WriteString("Dockerfile differences:\n")
			diffDetails.WriteString(cmp.Diff(legacyDockerfile, newDockerfile))
			diffDetails.WriteString("\n")
		}
		if !stepsEqual {
			diffDetails.WriteString("Build steps differences:\n")
			diffDetails.WriteString(cmp.Diff(legacyBuild.Steps, newBuildSteps))
		}
		return false, diffDetails.String()
	}
	return true, ""
}

// Combined fuzz test covering all scenarios: comprehensive parameters, whitespace edge cases, and boolean flags
func FuzzImplementationEquivalence(f *testing.F) {
	// Normal cases with different ecosystems
	f.Add("npm", "lodash", "4.17.21", "lodash-4.17.21.tgz", "git,nodejs,npm", "npm install", "npm run build", "dist/lodash.min.js", false, false, false)
	f.Add("pypi", "requests", "2.28.1", "requests-2.28.1.tar.gz", "git,python3,pip", "pip install -r requirements.txt", "python setup.py build", "dist/requests-2.28.1.tar.gz", false, false, false)
	f.Add("maven", "junit", "5.8.2", "junit-5.8.2.jar", "git,openjdk-11-jdk,maven", "mvn clean compile", "mvn package", "target/junit-5.8.2.jar", false, false, false)
	// Whitespace edge cases
	f.Add("npm", "test", "1.0.0", "test.tgz", "git, nodejs,  npm", "npm install   && npm audit fix", "npm run build   ", "  dist/output.tgz  ", false, false, false)
	f.Add("pypi", "test", "1.0.0", "test.tar.gz", "git,python3", "pip install -r requirements.txt\n", "\npython -m build", "dist/*.tar.gz", false, false, false)
	f.Add("maven", "test", "1.0.0", "test.jar", " git , maven ", "mvn clean && mvn compile", "mvn package -DskipTests", "target/test-*.jar", false, false, false)
	// Commands with tabs and newlines
	f.Add("npm", "complex", "1.0.0", "complex.tgz", "git,nodejs", "npm ci\nnpm run prepare", "npm run build\nnpm test", "build/output.tgz", false, false, false)
	f.Add("pypi", "complex", "1.0.0", "complex.whl", "git,python3,build", "python -m pip install build\npip install -e .", "python -m build --wheel", "dist/*.whl", false, false, false)
	// Additional whitespace patterns
	f.Add("npm", "whitespace", "1.0.0", "whitespace.tgz", "git,nodejs", "  npm install  ", "  npm run build  ", "dist/output.tgz", false, false, false)                            // leading/trailing spaces
	f.Add("npm", "tabs", "1.0.0", "tabs.tgz", "git,nodejs", "npm\tinstall", "npm\trun\tbuild", "dist/output.tgz", false, false, false)                                             // tabs
	f.Add("npm", "newlines", "1.0.0", "newlines.tgz", "git,nodejs", "npm install\n", "npm run build\n", "dist/output.tgz", false, false, false)                                    // trailing newlines
	f.Add("npm", "leading-nl", "1.0.0", "leading.tgz", "git,nodejs", "\nnpm install", "\nnpm run build", "dist/output.tgz", false, false, false)                                   // leading newlines
	f.Add("npm", "multiline", "1.0.0", "multiline.tgz", "git,nodejs", "npm install\nnpm audit", "npm run build\nnpm test", "dist/output.tgz", false, false, false)                 // multiline
	f.Add("npm", "extraspaces", "1.0.0", "extraspaces.tgz", "git,nodejs", "npm install   &&   npm audit", "npm run build   &&   npm test", "dist/output.tgz", false, false, false) // extra spaces around operators
	// Empty deps command edge case
	f.Add("npm", "minimal", "1.0.0", "minimal.tgz", "git", "", "npm pack", "*.tgz", false, false, false)
	f.Add("debian", "minimal", "1.0.0", "minimal.deb", "git,build-essential", "make clean", "make", "output.deb", false, false, false)
	// Boolean flag combinations (all 8 combinations)
	f.Add("npm", "flags-none", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", false, false, false)        // no features
	f.Add("npm", "flags-timewarp", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", true, false, false)     // timewarp only
	f.Add("npm", "flags-proxy", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", false, true, false)        // proxy only
	f.Add("npm", "flags-syscall", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", false, false, true)      // syscall only
	f.Add("npm", "flags-tw-proxy", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", true, true, false)      // timewarp + proxy
	f.Add("npm", "flags-tw-syscall", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", true, false, true)    // timewarp + syscall
	f.Add("npm", "flags-proxy-syscall", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", false, true, true) // proxy + syscall
	f.Add("npm", "flags-all", "1.0.0", "flags.tgz", "git,nodejs", "npm install", "npm run build", "dist/output.tgz", true, true, true)            // all features
	f.Fuzz(func(t *testing.T, ecosystem, pkg, version, artifact, systemDepsStr, deps, build, outputPath string, useTimewarp, useNetworkProxy, useSyscallMonitor bool) {
		// Convert string to ecosystem enum
		var eco rebuild.Ecosystem
		switch ecosystem {
		case "npm":
			eco = rebuild.NPM
		case "pypi":
			eco = rebuild.PyPI
		case "maven":
			eco = rebuild.Maven
		case "debian":
			eco = rebuild.Debian
		default:
			t.Skip("Unknown ecosystem")
		}
		// Skip obviously invalid inputs that would break both implementations
		if pkg == "" || version == "" || artifact == "" || outputPath == "" {
			t.Skip("Empty required fields")
		}
		// Skip inputs with null bytes which could cause issues
		containsNullByte := func(s string) bool { return strings.Contains(s, "\x00") }
		if containsNullByte(pkg) || containsNullByte(version) || containsNullByte(artifact) ||
			containsNullByte(deps) || containsNullByte(build) || containsNullByte(outputPath) {
			t.Skip("Contains null bytes")
		}
		// Skip cases where build is empty - both implementations should fail
		if strings.TrimSpace(build) == "" {
			t.Skip("Empty build command")
		}
		// Parse systemDeps from comma-separated string, handling whitespace
		var systemDeps []string
		if systemDepsStr != "" {
			parts := strings.Split(systemDepsStr, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					systemDeps = append(systemDeps, trimmed)
				}
			}
		}
		// Ensure we have at least git for basic functionality
		if len(systemDeps) == 0 {
			systemDeps = []string{"git"}
		}
		target := rebuild.Target{
			Ecosystem: eco,
			Package:   pkg,
			Version:   version,
			Artifact:  artifact,
		}
		strategy := rebuild.ManualStrategy{
			Location:   rebuild.Location{Repo: "github.com/example/repo", Ref: "main", Dir: "/src"},
			SystemDeps: systemDeps,
			Deps:       deps,
			Build:      build,
			OutputPath: outputPath,
		}
		opts := rebuild.RemoteOptions{
			Project:             "test-project",
			LogsBucket:          "test-logs",
			BuildServiceAccount: "test-account",
			PrebuildConfig:      rebuild.PrebuildConfig{Bucket: "test-bucket"},
			RemoteMetadataStore: rebuild.NewFilesystemAssetStore(memfs.New()),
			UseTimewarp:         useTimewarp,
			UseNetworkProxy:     useNetworkProxy,
			UseSyscallMonitor:   useSyscallMonitor,
		}
		equivalent, details := checkEquivalence(target, strategy, opts)
		if !equivalent {
			t.Errorf("Equivalence property failed for:")
			t.Errorf("  Target: %+v", target)
			t.Errorf("  SystemDeps: %q", systemDeps)
			t.Errorf("  Deps: %q (len=%d, bytes=%v)", deps, len(deps), []byte(deps))
			t.Errorf("  Build: %q (len=%d, bytes=%v)", build, len(build), []byte(build))
			t.Errorf("  OutputPath: %q", outputPath)
			t.Errorf("  Flags: UseTimewarp=%v, UseNetworkProxy=%v, UseSyscallMonitor=%v", useTimewarp, useNetworkProxy, useSyscallMonitor)
			t.Errorf("Details:\n%s", details)
		}
	})
}
