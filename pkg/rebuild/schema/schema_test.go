// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package schema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	yaml "gopkg.in/yaml.v3"
)

// Strategies and their encodings to be used by encode, decode and round trip tests.
var strategies = []struct {
	name        string
	strategy    rebuild.Strategy
	jsonEncoded string
	yamlEncoded string
}{

	{
		name: "PackBuildVersionOverride",
		strategy: &npm.NPMPackBuild{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
			NPMVersion:      "red",
			VersionOverride: "green",
		},
		jsonEncoded: `{"npm_pack_build":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","npm_version":"red","version_override":"green"}}`,
		yamlEncoded: `
npm_pack_build:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
  npmversion: red
  versionoverride: green
`,
	},
	{
		name: "NPMCustomBuild",
		strategy: &npm.NPMCustomBuild{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
			NPMVersion:      "red",
			VersionOverride: "green",
			Command:         "the_command",
			RegistryTime:    time.Time{},
		},
		jsonEncoded: `{"npm_custom_build":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","npm_version":"red","node_version":"","version_override":"green","command":"the_command","registry_time":"0001-01-01T00:00:00Z"}}`,
		yamlEncoded: `
npm_custom_build:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
  npmversion: red
  nodeversion: ""
  versionoverride: green
  command: the_command
  registrytime: 0001-01-01T00:00:00Z
`,
	},
	{
		name: "PureWheelBuild",
		strategy: &pypi.PureWheelBuild{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
			Requirements: []string{"req_a", "req_b"},
		},
		jsonEncoded: `{"pypi_pure_wheel_build":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","requirements":["req_a","req_b"]}}`,
		yamlEncoded: `
pypi_pure_wheel_build:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
  requirements:
    - req_a
    - req_b
`,
	},
	{
		name: "CratesioCargoPackage",
		strategy: &cratesio.CratesIOCargoPackage{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
			RustVersion: "some_version",
			ExplicitLockfile: &cratesio.ExplicitLockfile{
				LockfileBase64: "lock_base64",
			},
		},
		jsonEncoded: `{"cratesio_cargo_package":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","rust_version":"some_version","explicit_lockfile":{"lockfile_base64":"lock_base64"}}}`,
		yamlEncoded: `
cratesio_cargo_package:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
  rust_version: some_version
  explicit_lockfile:
    lockfile_base64: lock_base64
`,
	},
	{
		name: "CratesioCargoPackageNoLockfile",
		strategy: &cratesio.CratesIOCargoPackage{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
		},
		jsonEncoded: `{"cratesio_cargo_package":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","rust_version":"","explicit_lockfile":null}}`,
		yamlEncoded: `
cratesio_cargo_package:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
`,
	},
	{
		name: "ManualStrategy",
		strategy: &rebuild.ManualStrategy{
			Location: rebuild.Location{
				Dir:  "the_dir",
				Ref:  "the_ref",
				Repo: "the_repo",
			},
			Build: "foo",
			Deps:  "bar",
		},
		jsonEncoded: `{"manual":{"repo":"the_repo","ref":"the_ref","dir":"the_dir","deps":"bar","build":"foo","system_deps":null,"output_path":""}}`,
		yamlEncoded: `
manual:
  location:
    repo: the_repo
    ref: the_ref
    dir: the_dir
  deps: bar
  build: foo
`,
	},
}

func normalizeYML(yml string) string {
	return strings.TrimSpace(yml)
}

func TestYamlMarshalStrategy(t *testing.T) {
	for _, tc := range strategies {
		res := new(bytes.Buffer)
		enc := yaml.NewEncoder(res)
		enc.SetIndent(2)
		if err := enc.Encode(NewStrategyOneOf(tc.strategy)); err != nil {
			t.Errorf("%s Marshal StrategyOneOf(%v) failed: %v", tc.name, tc.strategy, err)
		}
		if got, want := normalizeYML(res.String()), normalizeYML(tc.yamlEncoded); got != want {
			t.Errorf("%s Marshal StrategyOneOf(%v) %v", tc.name, tc.strategy, cmp.Diff(got, want))
		}
	}
}

func TestJsonMarshalStrategy(t *testing.T) {
	for _, tc := range strategies {
		res, err := json.Marshal(NewStrategyOneOf(tc.strategy))
		if err != nil {
			t.Errorf("%s Marshal StrategyOneOf(%v) failed: %v", tc.name, tc.strategy, err)
		}
		if got, want := string(res), tc.jsonEncoded; got != want {
			t.Errorf("%s Marshal StrategyOneOf(%v) %v", tc.name, tc.strategy, cmp.Diff(got, want))
		}
	}
}

func TestYamlUnmarshalStrategy(t *testing.T) {
	for _, tc := range strategies {
		var oneof StrategyOneOf
		err := yaml.Unmarshal([]byte(tc.yamlEncoded), &oneof)
		if err != nil {
			t.Errorf("%s Unmarshal StrategyOneOf(%v) failed: %v", tc.name, tc.yamlEncoded, err)
		}
		var s rebuild.Strategy
		s, err = oneof.Strategy()
		if err != nil {
			t.Errorf("%s Unpacking StrategyOneOf(%v) failed: %v", tc.name, oneof, err)
		}
		if got, want := s, tc.strategy; !cmp.Equal(got, want) {
			t.Errorf("%s Unmarshal StrategyOneof diff(%v) \"%v\"", tc.name, tc.yamlEncoded, cmp.Diff(got, want))
		}
	}
}

func TestJsonUnmarshalStrategy(t *testing.T) {
	for _, tc := range strategies {
		var oneof StrategyOneOf
		err := json.Unmarshal([]byte(tc.jsonEncoded), &oneof)
		if err != nil {
			t.Errorf("%s Unmarshal StrategyOneOf(%v) failed: %v", tc.name, tc.jsonEncoded, err)
		}
		var s rebuild.Strategy
		s, err = oneof.Strategy()
		if err != nil {
			t.Errorf("%s Unpacking StrategyOneOf(%v) failed: %v", tc.name, oneof, err)
		}
		if got, want := s, tc.strategy; !cmp.Equal(got, want) {
			t.Errorf("%s Unmarshal StrategyOneof diff(%v) \"%v\"", tc.name, tc.jsonEncoded, cmp.Diff(got, want))
		}
	}
}

func TestYamlMarshalStrategyRoundTrip(t *testing.T) {
	for _, tc := range strategies {
		enc, err := yaml.Marshal(NewStrategyOneOf(tc.strategy))
		if err != nil {
			t.Errorf("%s Marshal StrategyOneOf(%v) failed: %v", tc.name, tc.strategy, err)
		}
		var resOneof StrategyOneOf
		err = yaml.Unmarshal(enc, &resOneof)
		if err != nil {
			t.Errorf("%s Unmarshal StrategyOneof(%v) failed: %v", tc.name, enc, err)
		}
		var res rebuild.Strategy
		res, err = resOneof.Strategy()
		if err != nil {
			t.Errorf("%s Unpacking StrategyOneOf(%v) failed: %v", tc.name, resOneof, err)
		}
		if got, want := res, tc.strategy; !cmp.Equal(got, want) {
			t.Errorf("RoundTrip(%v) %v", tc.strategy, cmp.Diff(got, want))
		}
	}
}

func TestJsonMarshalStrategyRoundTrip(t *testing.T) {
	for _, tc := range strategies {
		enc, err := json.Marshal(NewStrategyOneOf(tc.strategy))
		if err != nil {
			t.Errorf("%s Marshal StrategyOneOf(%v) failed: %v", tc.name, tc.strategy, err)
		}
		var resOneof StrategyOneOf
		err = json.Unmarshal(enc, &resOneof)
		if err != nil {
			t.Errorf("%s Unmarshal StrategyOneof(%v) failed: %v", tc.name, enc, err)
		}
		var res rebuild.Strategy
		res, err = resOneof.Strategy()
		if err != nil {
			t.Errorf("%s Unpacking StrategyOneOf(%v) failed: %v", tc.name, resOneof, err)
		}
		if got, want := res, tc.strategy; !cmp.Equal(got, want) {
			t.Errorf("RoundTrip(%v) %v", tc.strategy, cmp.Diff(got, want))
		}
	}
}
