// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestReplaceVersion(t *testing.T) {
	for _, tc := range []struct{ s, old, new, want string }{
		{"pkg-1.2.3.tgz", "1.2.3", "1.3.0", "pkg-1.3.0.tgz"},
		{"git checkout v1.2.3", "1.2.3", "1.3.0", "git checkout v1.3.0"},
		{"npm@11.2.3 and 1.2.3.4", "1.2.3", "1.3.0", "npm@11.2.3 and 1.2.3.4"},
		{"1.2.3", "1.2.3", "1.3.0", "1.3.0"},
		{"a 1.2.3", "", "1.3.0", "a 1.2.3"},
	} {
		if got := replaceVersion(tc.s, tc.old, tc.new); got != tc.want {
			t.Errorf("replaceVersion(%q, %q, %q) = %q, want %q", tc.s, tc.old, tc.new, got, tc.want)
		}
	}
}

func npmInference(node, npmVersion, registryTime string) *schema.StrategyOneOf {
	return &schema.StrategyOneOf{NPMCustomBuild: &npm.NPMCustomBuild{NodeVersion: node, NPMVersion: npmVersion, RegistryTime: mustTime(registryTime)}}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRelocate(t *testing.T) {
	seed := &schema.AgentSeed{
		Version: "9.6.1",
		Strategy: &schema.StrategyOneOf{ManualStrategy: &rebuild.ManualStrategy{
			Location:   rebuild.Location{Repo: "https://github.com/eslint/espree", Ref: "91ea3503"},
			Deps:       "wget https://unofficial-builds.nodejs.org/download/release/v18.16.1/node-v18.16.1-linux-x64-musl.tar.gz\nnpx --package=npm@9.5.1 -c 'npm_config_registry=http://npm:2023-07-14T15:47:50Z@localhost:8080 npm install'",
			Build:      "npx --package=npm@9.5.1 -c 'npm run prepublishOnly && npm pack'",
			OutputPath: "espree-9.6.1.tgz",
		}},
	}
	from := npmInference("18.16.1", "9.5.1", "2023-07-14T15:47:50.123Z")
	to := npmInference("22.16.0", "10.9.2", "2025-06-09T15:46:03.805Z")
	target := rebuild.Target{Ecosystem: rebuild.NPM, Package: "espree", Version: "10.4.0", Artifact: "espree-10.4.0.tgz"}
	loc := rebuild.Location{Repo: "https://github.com/eslint/js", Ref: "ab3d5938", Dir: "packages/espree"}
	got, err := relocate(seed, from, to, target, loc)
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	want := &rebuild.ManualStrategy{
		Location:   loc,
		Deps:       "wget https://unofficial-builds.nodejs.org/download/release/v22.16.0/node-v22.16.0-linux-x64-musl.tar.gz\nnpx --package=npm@10.9.2 -c 'npm_config_registry=http://npm:2025-06-09T15:46:03Z@localhost:8080 npm install'",
		Build:      "npx --package=npm@10.9.2 -c 'npm run prepublishOnly && npm pack'",
		OutputPath: "espree-10.4.0.tgz",
	}
	if diff := cmp.Diff(want, got.ManualStrategy); diff != "" {
		t.Errorf("relocate (-want +got):\n%s", diff)
	}
	// Without the seed's inference proposal, only the source and version move.
	got, err = relocate(seed, nil, to, target, loc)
	if err != nil {
		t.Fatalf("relocate without origin: %v", err)
	}
	if !strings.Contains(got.ManualStrategy.Deps, "node-v18.16.1") || got.ManualStrategy.OutputPath != "espree-10.4.0.tgz" {
		t.Errorf("relocate without origin = %+v", got.ManualStrategy)
	}
}

func TestSeedProposalOnceAfterInference(t *testing.T) {
	seed := &schema.AgentSeed{Version: "1.0.0",
		Strategy: &schema.StrategyOneOf{ManualStrategy: &rebuild.ManualStrategy{Build: "npm pack", OutputPath: "p-1.0.0.tgz"}}}
	a := &defaultAgent{
		t:    rebuild.Target{Ecosystem: rebuild.NPM, Package: "p", Version: "1.1.0", Artifact: "p-1.1.0.tgz"},
		loc:  rebuild.Location{Repo: "https://example.com/p", Ref: "abc"},
		deps: &AgentDeps{Seed: seed},
	}
	if s := a.seedProposal(); s != nil {
		t.Fatalf("seed proposed before the inference attempt")
	}
	a.iterHistory = []*schema.AgentIteration{{Strategy: npmInference("22.16.0", "10.9.2", "2025-06-09T15:46:03.805Z")}}
	s := a.seedProposal()
	if s == nil || s.ManualStrategy == nil || s.ManualStrategy.OutputPath != "p-1.1.0.tgz" || !cmp.Equal(s.ManualStrategy.Location, a.loc) {
		t.Fatalf("seedProposal = %+v", s)
	}
	if s := a.seedProposal(); s != nil {
		t.Errorf("seed proposed twice")
	}
	if got := strings.Join(a.renderSeed(seed), "\n"); !strings.Contains(got, "An earlier attempt in this session ran that build") {
		t.Errorf("renderSeed after the build = %q", got)
	}
}
