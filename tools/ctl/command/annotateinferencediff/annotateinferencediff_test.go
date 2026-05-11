// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package annotateinferencediff

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	billyutil "github.com/go-git/go-billy/v5/util"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDiffStrategies(t *testing.T) {
	mkPWB := func(ref, py string, reqs []string, ts time.Time) *pypi.PureWheelBuild {
		return &pypi.PureWheelBuild{
			Location:      rebuild.Location{Repo: "r", Ref: ref},
			PythonVersion: py,
			Requirements:  reqs,
			RegistryTime:  ts,
		}
	}
	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		rec, inf rebuild.Strategy
		// Exhaustive field→body map. Body "" means "field is expected but
		// body is not pinned" (the got body is redacted before compare).
		want map[string]string
	}{
		{
			name: "identical",
			rec:  mkPWB("aaa", "", []string{"a", "b"}, ts),
			inf:  mkPWB("aaa", "", []string{"a", "b"}, ts),
			want: map[string]string{},
		},
		{
			name: "ref differs",
			rec:  mkPWB("abcdef0123456789", "", []string{"a"}, ts),
			inf:  mkPWB("ffffffffffffffff", "", []string{"a"}, ts),
			want: map[string]string{"ref": "abcdef0123456789 (inferred: ffffffffffffffff)"},
		},
		{
			name: "requirements set-diff",
			rec:  mkPWB("aaa", "", []string{"a", "b"}, ts),
			inf:  mkPWB("aaa", "", []string{"a", "b", "c"}, ts),
			want: map[string]string{"requirements": "removed c (vs inferred)"},
		},
		{
			// Source/deps render identically; only the build command diverges.
			// Body redacted — exact rendered shell is not pinned here.
			name: "typed-vs-typed canonicalizes to workflow",
			rec:  mkPWB("aaa", "", nil, ts),
			inf:  &pypi.SdistBuild{Location: rebuild.Location{Repo: "r", Ref: "aaa"}, Requirements: nil, RegistryTime: ts},
			want: map[string]string{"build diff": ""},
		},
		{
			name: "typed-vs-equivalent-workflow",
			rec:  mkPWB("aaa", "", []string{"a"}, ts),
			inf:  mkPWB("aaa", "", []string{"a"}, ts).ToWorkflow(),
			want: map[string]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diffs, err := diffStrategies(tc.rec, tc.inf)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			got := map[string]string{}
			for _, d := range diffs {
				got[d.field] = d.body
			}
			for k, v := range tc.want {
				if v == "" {
					if _, ok := got[k]; ok {
						got[k] = ""
					}
				}
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestStripExistingHeader(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no header passes through",
			input: "pypi_pure_wheel_build:\n  location:\n    repo: x\n",
			want:  "pypi_pure_wheel_build:\n  location:\n    repo: x\n",
		},
		{
			name:  "differs header stripped",
			input: "# inference differs:\n#   ref: a (inferred: b)\npypi_pure_wheel_build:\n  ref: x\n",
			want:  "pypi_pure_wheel_build:\n  ref: x\n",
		},
		{
			name:  "fails header stripped",
			input: "# inference fails: no git ref\npypi_pure_wheel_build:\n  ref: x\n",
			want:  "pypi_pure_wheel_build:\n  ref: x\n",
		},
		{
			name: "internal comments preserved",
			input: "# inference differs:\n#   ref: a (inferred: b)\npypi_pure_wheel_build:\n  location:\n" +
				"    # comment about ref\n    ref: x\n",
			want: "pypi_pure_wheel_build:\n  location:\n    # comment about ref\n    ref: x\n",
		},
		{
			name: "user comments around header preserved",
			input: "# NOTE: pre-header user note\n# inference differs:\n#   ref: a (inferred: b)\n" +
				"# NOTE: post-header user note\npypi_pure_wheel_build:\n  ref: x\n",
			want: "# NOTE: pre-header user note\n# NOTE: post-header user note\npypi_pure_wheel_build:\n  ref: x\n",
		},
		{
			name: "multiline differs block stripped, trailing user note kept",
			input: "# inference differs:\n#   ref: a (inferred: b)\n#  source diff:\n" +
				"#    git checkout\n#   +rm -rf /src/.git\n# NOTE: keep me\npypi_pure_wheel_build:\n  ref: x\n",
			want: "# NOTE: keep me\npypi_pure_wheel_build:\n  ref: x\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripExistingHeader([]byte(tc.input)))
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestDiffStrategies_NestedSteps(t *testing.T) {
	mkWF := func(deps []flow.Step) *rebuild.WorkflowStrategy {
		return &rebuild.WorkflowStrategy{
			Location: rebuild.Location{Repo: "r", Ref: "abc"},
			Source:   []flow.Step{{Uses: "git-checkout"}},
			Deps:     deps,
			Build:    []flow.Step{{Uses: "pypi/build/wheel"}},
		}
	}
	stepVenv := flow.Step{Uses: "pypi/setup-venv", With: map[string]string{"path": "/deps", "pythonVersion": "3.11"}}
	stepVenv310 := flow.Step{Uses: "pypi/setup-venv", With: map[string]string{"path": "/deps", "pythonVersion": "3.10"}}
	stepRegistry := flow.Step{Uses: "pypi/setup-registry", With: map[string]string{"registryTime": "2025-01-01T00:00:00Z"}}
	stepInstall := flow.Step{Runs: "/deps/bin/pip install build"}
	stepExtra := flow.Step{Runs: "rm -rf /src/.git"}
	// stepBasic supplies enough With params for `pypi/deps/basic` to render
	// without errors (install-deps's template requires valid-JSON requirements).
	stepBasic := flow.Step{Uses: "pypi/deps/basic", With: map[string]string{
		"venv":          "/deps",
		"pythonVersion": "",
		"registryTime":  "2025-01-01T00:00:00Z",
		"requirements":  "[]",
	}}

	tests := []struct {
		name     string
		rec, inf *rebuild.WorkflowStrategy
		expect   [][2]string // (field substring, body substring) pairs to find
		nDiffs   int         // -1 to skip the count check
	}{
		// WorkflowStrategy diffs surface as multiline `<phase> diff` entries
		// over the resolved shell. Per-step structural drilldown is gone.
		{
			name:   "param replaced inside a step",
			rec:    mkWF([]flow.Step{stepVenv, stepRegistry, stepInstall}),
			inf:    mkWF([]flow.Step{stepVenv310, stepRegistry, stepInstall}),
			expect: [][2]string{{"deps diff", "--python 3.11"}, {"deps diff", "--python 3.10"}},
			nDiffs: 1,
		},
		{
			// inferred has extra registry step → line shows as `-` (removed
			// going inferred → manual).
			name: "step added in inferred (manual missing it)",
			rec:  mkWF([]flow.Step{stepVenv, stepInstall}),
			inf:  mkWF([]flow.Step{stepVenv, stepRegistry, stepInstall}),
			expect: [][2]string{
				{"deps diff", "-export PIP_INDEX_URL"},
			},
			nDiffs: 1,
		},
		{
			// manual has extra rm step → line shows as `+`.
			name: "step removed in inferred (manual has extra step)",
			rec:  mkWF([]flow.Step{stepVenv, stepRegistry, stepExtra, stepInstall}),
			inf:  mkWF([]flow.Step{stepVenv, stepRegistry, stepInstall}),
			expect: [][2]string{
				{"deps diff", "+rm -rf /src/.git"},
			},
			nDiffs: 1,
		},
		{
			// Manual's primitive trio vs inferred's pypi/deps/basic — after
			// the install-deps tool resolves and the pip-install normalizer
			// collapses, only the venv-setup line differs (manual omits
			// pythonVersion, so it falls into the `python3 -m venv` branch
			// while inferred renders via `uv venv --python ""`).
			name: "block replaced (different deps shape entirely)",
			rec:  mkWF([]flow.Step{stepVenv, stepRegistry, stepInstall}),
			inf:  mkWF([]flow.Step{stepBasic}),
			expect: [][2]string{
				{"deps diff", "uv venv"},
				{"deps diff", "python3 -m venv"},
			},
			nDiffs: 1,
		},
		{
			// pypi/deps/basic on both sides; requirements differs by `c`.
			// install-deps emits one `pip install` per requirement, then the
			// pip-install normalizer collapses to a single sorted line.
			name: "nested set-diff inside a step's with.requirements",
			rec: mkWF([]flow.Step{{
				Uses: "pypi/deps/basic",
				With: map[string]string{"requirements": `["a","b"]`},
			}}),
			inf: mkWF([]flow.Step{{
				Uses: "pypi/deps/basic",
				With: map[string]string{"requirements": `["a","b","c"]`},
			}}),
			expect: [][2]string{
				{"deps diff", "-/bin/pip install a b build c"},
				{"deps diff", "+/bin/pip install a b build"},
			},
			nDiffs: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diffs, err := diffStrategies(tc.rec, tc.inf)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if tc.nDiffs >= 0 && len(diffs) != tc.nDiffs {
				t.Errorf("got %d diffs, want %d:", len(diffs), tc.nDiffs)
				for _, d := range diffs {
					t.Errorf("    %s: %s", d.field, d.body)
				}
			}
			for _, want := range tc.expect {
				wField, wBody := want[0], want[1]
				found := false
				for _, d := range diffs {
					if strings.Contains(d.field, wField) && strings.Contains(d.body, wBody) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing diff matching field~%q body~%q", wField, wBody)
					for _, d := range diffs {
						t.Logf("    got: %s: %s", d.field, d.body)
					}
				}
			}
		})
	}
}

func TestHandler(t *testing.T) {
	recordedYAML := `pypi_pure_wheel_build:
  location:
    repo: https://github.com/x/y
    ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  python_version: ""
  requirements:
    - a
  registry_time: "2025-01-01T00:00:00Z"
`
	// Bare StrategyOneOf JSON (the shape `ctl infer --format=strategy` emits).
	matchingStrategy := `{"pypi_pure_wheel_build":{"repo":"https://github.com/x/y","ref":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dir":"","python_version":"","requirements":["a"],"registry_time":"2025-01-01T00:00:00Z"}}`
	differingStrategy := `{"pypi_pure_wheel_build":{"repo":"https://github.com/x/y","ref":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","dir":"","python_version":"","requirements":["a"],"registry_time":"2025-01-01T00:00:00Z"}}`
	// google.rpc.Status protojson (the shape --format=strategy-or-status emits
	// on failure).
	failingStatus := `{"code":13,"message":"no git ref"}`
	// Same failure, with the wrapper prefixes that cleanInferenceMessage strips.
	wrappedFailingStatus := `{"code":13,"message":"failed to infer strategy: [INTERNAL] Failed to get upstream generator: unsupported generator: pdm-backend (2.4.5)"}`

	staleHeaderYAML := "# inference differs:\n#   ref: zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz (inferred: yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy)\n" + recordedYAML
	noStrategyYAML := "custom_stabilizers:\n  - name: example\n    config: {}\n"

	type want struct {
		err           error    // matched with errors.Is
		errContains   string   // substring of err.Error()
		fileContains  string   // substring expected in final file content
		fileExcludes  []string // substrings that must NOT appear in final file
		fileEqual     string   // exact final file content
		fileUnchanged bool     // file must be byte-for-byte identical to start
		stderrEqual   string   // exact contents of stderr (empty = unchecked)
	}

	tests := []struct {
		name         string
		startingYAML string // file content at /build.yaml; defaults to recordedYAML
		inferred     string // contents of /inferred.json
		stdin        string // contents of stdin (used when InferencePath="-")
		cfg          Config
		w            want
	}{
		{
			name:     "match: file unchanged",
			inferred: matchingStrategy,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w:        want{fileUnchanged: true},
		},
		{
			name:     "diff: header prepended",
			inferred: differingStrategy,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w: want{
				fileContains: "# inference differs:\n#   ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (inferred: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)",
			},
		},
		{
			name:     "fail: header prepended (auto-detected as status)",
			inferred: failingStatus,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w:        want{fileContains: "# inference fails: no git ref"},
		},
		{
			name:     "fail: header trims generic wrappers, leaves generator-specific text",
			inferred: wrappedFailingStatus,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w:        want{fileContains: "# inference fails: Failed to get upstream generator: unsupported generator: pdm-backend (2.4.5)"},
		},
		{
			name:  "stdin (strategy)",
			stdin: matchingStrategy,
			cfg:   Config{InferencePath: "-", BuildYAML: "/build.yaml"},
			w:     want{fileUnchanged: true},
		},
		{
			name:  "stdin (status)",
			stdin: failingStatus,
			cfg:   Config{InferencePath: "-", BuildYAML: "/build.yaml"},
			w:     want{fileContains: "# inference fails: no git ref"},
		},
		{
			name:     "check fails on drift",
			inferred: differingStrategy,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml", Check: true},
			w:        want{err: errCheckFailed, fileUnchanged: true},
		},
		{
			name:     "check passes when in sync",
			inferred: matchingStrategy,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml", Check: true},
			w:        want{fileUnchanged: true},
		},
		{
			name:     "dry-run skips write despite drift",
			inferred: differingStrategy,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml", DryRun: true},
			w:        want{fileUnchanged: true},
		},
		{
			name:     "malformed inference input is an error",
			inferred: `not json`,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w:        want{errContains: "decoding inference output", fileUnchanged: true},
		},
		{
			// OK-status falls through to strategy decode and surfaces as a
			// downstream error rather than being silently accepted.
			name:     "status with OK code is rejected downstream",
			inferred: `{"code":0,"message":""}`,
			cfg:      Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w:        want{errContains: "extracting inferred strategy", fileUnchanged: true},
		},
		{
			name:         "rerun replaces stale differs header in place",
			startingYAML: staleHeaderYAML,
			inferred:     differingStrategy,
			cfg:          Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w: want{
				fileContains: "# inference differs:\n#   ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (inferred: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)",
				fileExcludes: []string{"zzzzzzzzzzzzzzzzzzzzzzzz", "yyyyyyyyyyyyyyyyyyyyyyyy"},
			},
		},
		{
			name:         "rerun strips stale header when content now matches",
			startingYAML: staleHeaderYAML,
			inferred:     matchingStrategy,
			cfg:          Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w: want{
				fileEqual:   recordedYAML,
				stderrEqual: "match /build.yaml (stale header removed)\n",
			},
		},
		{
			// --check fails on stale-but-matching: file would be rewritten to
			// strip the header. Log should name the stale-header cause, not
			// claim a plain "match".
			name:         "check: stale-but-matching header fails with diagnostic",
			startingYAML: staleHeaderYAML,
			inferred:     matchingStrategy,
			cfg:          Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml", Check: true},
			w: want{
				err:           errCheckFailed,
				fileUnchanged: true,
				stderrEqual:   "match /build.yaml (stale header removed)\n",
			},
		},
		{
			name:         "skip: no strategy in file",
			startingYAML: noStrategyYAML,
			inferred:     matchingStrategy,
			cfg:          Config{InferencePath: "/inferred.json", BuildYAML: "/build.yaml"},
			w: want{
				fileUnchanged: true,
				stderrEqual:   "skip /build.yaml: no strategy in file\n",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := memfs.New()
			startingYAML := tc.startingYAML
			if startingYAML == "" {
				startingYAML = recordedYAML
			}
			mustWrite(t, fs, "/build.yaml", startingYAML)
			if tc.inferred != "" {
				mustWrite(t, fs, "/inferred.json", tc.inferred)
			}
			var out, errBuf bytes.Buffer
			deps := &Deps{
				FS: fs,
				IO: cli.IO{In: strings.NewReader(tc.stdin), Out: &out, Err: &errBuf},
			}
			_, gotErr := Handler(context.Background(), tc.cfg, deps)
			switch {
			case tc.w.err != nil && gotErr == nil:
				t.Fatalf("want err %v, got nil", tc.w.err)
			case tc.w.err == nil && tc.w.errContains == "" && gotErr != nil:
				t.Fatalf("unexpected err: %v", gotErr)
			case tc.w.err != nil && !errors.Is(gotErr, tc.w.err):
				t.Errorf("err mismatch: got %v, want errors.Is(%v)", gotErr, tc.w.err)
			case tc.w.errContains != "" && (gotErr == nil || !strings.Contains(gotErr.Error(), tc.w.errContains)):
				t.Errorf("err mismatch: got %v, want substring %q", gotErr, tc.w.errContains)
			}
			final := mustRead(t, fs, "/build.yaml")
			switch {
			case tc.w.fileUnchanged && final != startingYAML:
				t.Errorf("file changed but expected unchanged.\ngot:\n%s\nwant:\n%s", final, startingYAML)
			case tc.w.fileEqual != "" && final != tc.w.fileEqual:
				t.Errorf("file mismatch.\ngot:\n%s\nwant:\n%s", final, tc.w.fileEqual)
			case tc.w.fileContains != "" && !strings.Contains(final, tc.w.fileContains):
				t.Errorf("file missing %q\ngot:\n%s", tc.w.fileContains, final)
			}
			for _, exc := range tc.w.fileExcludes {
				if strings.Contains(final, exc) {
					t.Errorf("file unexpectedly contains %q\ngot:\n%s", exc, final)
				}
			}
			if tc.w.stderrEqual != "" && errBuf.String() != tc.w.stderrEqual {
				t.Errorf("stderr mismatch.\ngot:\n%q\nwant:\n%q", errBuf.String(), tc.w.stderrEqual)
			}
		})
	}
}

func mustWrite(t *testing.T, fs billy.Filesystem, path, content string) {
	t.Helper()
	if err := billyutil.WriteFile(fs, path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func mustRead(t *testing.T, fs billy.Filesystem, path string) string {
	t.Helper()
	b, err := billyutil.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// Covers the `!ina` / `!inb` branches in diffMaps, which the typed-strategy
// callers don't easily reach (their JSON shapes are symmetric).
func TestDiffMaps_AsymmetricKeys(t *testing.T) {
	manual := map[string]any{
		"only_in_manual": "M",
		"shared":         "same",
		"both_differ":    "manual_val",
	}
	inferred := map[string]any{
		"only_in_inferred": "I",
		"shared":           "same",
		"both_differ":      "inferred_val",
	}
	got := map[string]string{}
	for _, d := range diffMaps("", manual, inferred) {
		got[d.field] = d.body
	}
	want := map[string]string{
		"only_in_manual":   "M (inferred: unset)",
		"only_in_inferred": "(unset, inferred: I)",
		"both_differ":      "manual_val (inferred: inferred_val)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestSetDiff(t *testing.T) {
	cases := []struct {
		rec, inf []string
		want     string
	}{
		{
			rec: []string{"a", "b"}, inf: []string{"a", "b", "c"},
			want: "removed c (vs inferred)",
		},
		{
			rec: []string{"a", "b", "c"}, inf: []string{"a"},
			want: "added b, c (vs inferred)",
		},
		{
			rec: []string{"a", "x"}, inf: []string{"a", "y"},
			want: "added x; removed y (vs inferred)",
		},
	}
	for _, tc := range cases {
		got := setDiff(tc.rec, tc.inf)
		if got != tc.want {
			t.Errorf("setDiff(%v,%v) = %q want %q", tc.rec, tc.inf, got, tc.want)
		}
	}
}

func TestNormalizePipInstalls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "collapses install-deps multi-line into one sorted line",
			in:   "/deps/bin/pip install build\n/deps/bin/pip install 'flit==3.7.1'\n/deps/bin/pip install 'flit_core==3.7.1'\n",
			want: "/deps/bin/pip install build flit==3.7.1 flit_core==3.7.1\n",
		},
		{
			name: "leaves single combined call alone (up to sort/dedupe)",
			in:   "/deps/bin/pip install build flit_core==3.7.1 flit==3.7.1\n",
			want: "/deps/bin/pip install build flit==3.7.1 flit_core==3.7.1\n",
		},
		{
			name: "skips lines with flags (e.g. --no-deps)",
			in:   "/deps/bin/pip install --no-deps setuptools==60.10.0 wheel==0.37.1\n/deps/bin/pip install build\n",
			want: "/deps/bin/pip install --no-deps setuptools==60.10.0 wheel==0.37.1\n/deps/bin/pip install build\n",
		},
		{
			name: "preserves surrounding non-pip lines",
			in:   "/usr/bin/python3 -m venv /deps\n/deps/bin/pip install build\n/deps/bin/pip install 'X'\ncd /src && make\n",
			want: "/usr/bin/python3 -m venv /deps\n/deps/bin/pip install X build\ncd /src && make\n",
		},
		{
			name: "ignores `cd && pip install` (shell prefix has whitespace)",
			in:   "cd /src && /deps/bin/pip install foo\n",
			want: "cd /src && /deps/bin/pip install foo\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizePipInstalls(tc.in)
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// End-to-end check on the workflow path: emitted entries are multiline
// `<phase> diff` blocks, and the pip-install normaliser cancels out a
// raw `pip install` vs `pypi/deps/basic` rendering of the same packages.
func TestDiffStrategies_RenderedPath(t *testing.T) {
	loc := rebuild.Location{Repo: "r", Ref: "abcdef0123456789"}
	mkWF := func(src, deps, build []flow.Step) *rebuild.WorkflowStrategy {
		return &rebuild.WorkflowStrategy{Location: loc, Source: src, Deps: deps, Build: build}
	}
	manSrc := []flow.Step{{Uses: "git-checkout"}, {Runs: "rm -rf /src/.git"}}
	infSrc := []flow.Step{{Uses: "git-checkout"}}
	manDeps := []flow.Step{
		{Uses: "pypi/setup-venv", With: map[string]string{"locator": "/usr/bin/", "path": "/deps"}},
		{Uses: "pypi/setup-registry", With: map[string]string{"registryTime": "2025-01-01T00:00:00Z"}},
		{Runs: "/deps/bin/pip install foo==1.0 bar==2.0 build"},
	}
	infDeps := []flow.Step{{Uses: "pypi/deps/basic", With: map[string]string{
		"venv":          "/deps",
		"pythonVersion": "",
		"registryTime":  "2025-01-01T00:00:00Z",
		"requirements":  `["foo==1.0","bar==2.0"]`,
	}}}
	manBuild := []flow.Step{{Runs: "ENV=1 /deps/bin/python3 -m build --wheel -n"}}
	infBuild := []flow.Step{{Uses: "pypi/build/wheel", With: map[string]string{"locator": "/deps/bin/"}}}

	diffs, err := diffStrategies(mkWF(manSrc, manDeps, manBuild), mkWF(infSrc, infDeps, infBuild))
	if err != nil {
		t.Fatalf("diffStrategies: %v", err)
	}
	// All entries should be multiline phase diffs; deps phase should be
	// absent (manual & inferred pip-installs match after normalisation).
	got := map[string]string{}
	for _, d := range diffs {
		got[d.field] = d.body
		if !d.multiline {
			t.Errorf("expected multiline entry, got scalar: %s: %s", d.field, d.body)
		}
	}
	if _, ok := got["source diff"]; !ok {
		t.Errorf("missing `source diff` entry; got %v", keysOf(got))
	}
	if body := got["source diff"]; !strings.Contains(body, "rm -rf /src/.git") {
		t.Errorf("source diff missing rm -rf line; got: %s", body)
	}
	if _, ok := got["deps diff"]; ok {
		t.Errorf("deps diff should be absent after pip-install normalisation; got: %s", got["deps diff"])
	}
	if body, ok := got["build diff"]; !ok || !strings.Contains(body, "+ENV=1") {
		t.Errorf("build diff missing ENV=1 line; got: %v", got)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestFormatDiffersHeader_Multiline(t *testing.T) {
	got := formatDiffersHeader([]diff{
		{field: "ref", body: "abcdef0123456789 (inferred: ffffffffffffffff)"},
		{field: "source diff", multiline: true, body: " git checkout --force 'X'\n+rm -rf /src/.git\n"},
	})
	want := "# inference differs:\n" +
		"#   ref: abcdef0123456789 (inferred: ffffffffffffffff)\n" +
		"#  source diff:\n" +
		"#    git checkout --force 'X'\n" +
		"#   +rm -rf /src/.git\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
