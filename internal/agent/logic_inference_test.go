// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestToolchainStrategy(t *testing.T) {
	loc := rebuild.Location{Repo: "https://github.com/example/pkg", Ref: "abc123", Dir: "packages/pkg"}
	published := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	env := rebuild.BuildEnv{TimewarpHost: "localhost:8080"}
	// holds reports whether the script holds want, where an empty want means an empty script.
	holds := func(script, want string) bool {
		return script == want || (want != "" && strings.Contains(script, want))
	}
	for eco, want := range map[rebuild.Ecosystem]struct{ deps, build string }{
		rebuild.NPM:      {"node-v24.20.0", "set registry http://npm:2023-01-01T12:00:00Z@localhost:8080"},
		rebuild.PyPI:     {"venv /deps", "PIP_INDEX_URL=http://pypi:2023-01-01T12:00:00Z@localhost:8080/simple"},
		rebuild.CratesIO: {"rustup-init", ""},
		rebuild.Maven:    {"", ""},
	} {
		t.Run(string(eco), func(t *testing.T) {
			target := rebuild.Target{Ecosystem: eco, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"}
			inst, err := toolchainStrategy(eco, loc, published).GenerateFor(target, env)
			if err != nil {
				t.Fatalf("GenerateFor: %v", err)
			}
			if inst.Source == "" || inst.Location != loc {
				t.Errorf("source = %q at %+v, want a checkout of %+v", inst.Source, inst.Location, loc)
			}
			if !holds(inst.Deps, want.deps) {
				t.Errorf("deps = %q, want %q", inst.Deps, want.deps)
			}
			if !holds(inst.Build, want.build) {
				t.Errorf("build = %q, want only the registry pin %q", inst.Build, want.build)
			}
			if inst.OutputPath != "packages/pkg/pkg-1.0.0.tgz" {
				t.Errorf("output = %q", inst.OutputPath)
			}
		})
	}
	target := rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"}
	if inst, err := toolchainStrategy(rebuild.NPM, loc, time.Time{}).GenerateFor(target, env); err != nil || inst.Build != "" {
		t.Errorf("without a publish time: build = %q, err = %v, want no build", inst.Build, err)
	}
}

func TestHistoryContextNamesInferenceFailure(t *testing.T) {
	a := &defaultAgent{inferenceFailure: "rust version unsupported in MUSL builds"}
	prompt := strings.Join(a.historyContext(nil), "\n")
	if !strings.Contains(prompt, "## Inference") || !strings.Contains(prompt, "rust version unsupported in MUSL builds") {
		t.Errorf("prompt lacks the inference section:\n%s", prompt)
	}
	if strings.Contains(strings.Join((&defaultAgent{}).historyContext(nil), "\n"), "## Inference") {
		t.Error("inference section present without a failure")
	}
}
