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

package rebuild

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pkg/errors"
)

// Location is where a set of rebuild instruction should be executed.
type Location struct {
	Repo string `json:"repo" yaml:"repo"`
	Ref  string `json:"ref" yaml:"ref"`
	Dir  string `json:"dir" yaml:"dir,omitempty"`
}

// Instructions represents the source, dependencies, and build steps to execute a rebuild.
type Instructions struct {
	// The location these instructions should be executed from.
	Location   Location
	SystemDeps []string
	Source     string
	Deps       string
	Build      string
	// Where the generated artifact can be found.
	OutputPath string
}

// BuildEnv contains resources provided by the build environment that a strategy may use.
type BuildEnv struct {
	TimewarpHost           string
	HasRepo                bool
	PreferPreciseToolchain bool
}

// TimewarpURL constructs the correct URL for this ecosystem and registryTime.
func (b *BuildEnv) TimewarpURL(ecosystem string, registryTime time.Time) (string, error) {
	if b.TimewarpHost == "" {
		return "", errors.New("TimewarpHost hasn't been configured for this BuildEnv")
	}
	return fmt.Sprintf("http://%s:%s@%s", ecosystem, registryTime.Format(time.RFC3339), b.TimewarpHost), nil
}

// Strategy generates instructions to execute a rebuild.
type Strategy interface {
	GenerateFor(Target, BuildEnv) (Instructions, error)
}

// PopulateTemplate is a helper to execute a template string using a data object.
func PopulateTemplate(tmpl string, data any) (string, error) {
	tmpl = strings.TrimSpace(tmpl)
	t, err := template.New("buildTmpl").Funcs(
		template.FuncMap{
			"SemverCmp": semver.Cmp,
			"join":      func(sep string, s []string) string { return strings.Join(s, sep) },
		}).Parse(tmpl)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse template with contents: %s", tmpl)
	}
	output := new(bytes.Buffer)
	err = t.Execute(output, data)
	if err != nil {
		return "", errors.Wrapf(err, "failed to execute template with contents: %s", tmpl)
	}
	return output.String(), nil
}

// BasicSourceSetup provides a common source setup script.
func BasicSourceSetup(s Location, env *BuildEnv) (string, error) {
	if env.HasRepo {
		return PopulateTemplate("git checkout --force '{{.Ref}}'", s)
	}
	// TODO: We should eventually support single commit checkout.
	// This would be roughly:
	//   git init .
	//   git remote add origin '{{.Repo}}'
	//   git fetch --depth 1 origin '{{.Ref}}'
	//   git checkout FETCH_HEAD
	return PopulateTemplate(`
git clone '{{.Repo}}' .
git checkout --force '{{.Ref}}'
`, s)
}

// ExecuteScript executes a single step of the strategy and returns the output regardless of error.
func ExecuteScript(ctx context.Context, dir string, script string) (string, error) {
	output := new(bytes.Buffer)
	outAndLog := io.MultiWriter(output, log.Default().Writer())
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Stdout = outAndLog
	cmd.Stderr = outAndLog
	// CD into the package's directory (which is where we cloned the repo.)
	cmd.Dir = dir
	log.Printf(`Executing build script: """%s"""`, cmd.String())
	err := cmd.Run()
	return output.String(), err
}

// LocationHint is a partial strategy used to provide a hint (git repo, git ref) to the inference machinery, but it is not sufficient for execution.
type LocationHint struct {
	Location
}

// GenerateFor is unsupported for LocationHint.
func (s *LocationHint) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	return Instructions{}, errors.New("LocationHint must be expanded using inference")
}

var _ Strategy = &LocationHint{}
