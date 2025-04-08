// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package flow

import (
	"bytes"
	"cmp"
	"maps"
	"regexp"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pkg/errors"
)

// Step represents a simple or composite script template for use in a build process.
type Step struct {
	// Simple step: Templated shell script with declared deps.

	Runs  string   `json:"runs" yaml:"runs,omitempty"`
	Needs []string `json:"needs" yaml:"needs,omitempty"`

	// Composite step: Tool invocation with provided args.

	Uses string            `json:"uses" yaml:"uses,omitempty"`
	With map[string]string `json:"with" yaml:"with,omitempty"`
}

func resolveTemplate(buf *bytes.Buffer, tmpl string, data any) error {
	t, err := template.New("").Option("missingkey=zero").Funcs(template.FuncMap{
		"fromJSON":  FromJSON,
		"toJSON":    ToJSON,
		"cmpSemver": semver.Cmp,
		"regexReplace": func(src, pattern, repl string) (string, error) {
			if re, err := regexp.Compile(pattern); err != nil {
				return "", errors.Wrap(err, "compiling regex")
			} else {
				return string(re.ReplaceAll([]byte(src), []byte(repl))), nil
			}
		},
	}).Parse(tmpl)
	if err != nil {
		return errors.Wrap(err, "parsing template")
	}
	if err := t.Execute(buf, data); err != nil {
		return errors.Wrap(err, "executing template")
	}
	return nil
}

func joinMaps[K comparable, V any](base map[K]V, overrides map[K]V) map[K]V {
	result := make(map[K]V, len(base))
	maps.Copy(result, base)
	maps.Copy(result, overrides)
	return result
}

func (step Step) Resolve(with map[string]string, data Data) (Fragment, error) {
	hasRuns := step.Runs != ""
	hasUses := step.Uses != ""

	// NOTE: This is costly-but-not-significant at the expected scale.
	dataAndWith := joinMaps(data, map[string]any{"With": with})
	switch {
	case hasRuns == hasUses:
		return Fragment{}, errors.New("must provide exactly one of 'runs' or 'uses'")
	case hasRuns:
		buf := bytes.NewBuffer(nil)
		if err := resolveTemplate(buf, step.Runs, dataAndWith); err != nil {
			return Fragment{}, errors.Wrapf(err, "resolving 'runs' value")
		}
		return Fragment{Script: buf.String(), Needs: step.Needs}, nil
	case hasUses:
		tool, err := Tools.Get(step.Uses)
		if err != nil {
			return Fragment{}, err
		}
		resolvedWith := make(map[string]string, len(step.With))
		buf := bytes.NewBuffer(nil)
		for k, v := range step.With {
			if err := resolveTemplate(buf, v, dataAndWith); err != nil {
				return Fragment{}, errors.Wrapf(err, "resolving 'with' value for {key=%q,val=%q}", k, v)
			}
			resolvedWith[k] = buf.String()
			buf.Reset()
		}
		return tool.Generate(resolvedWith, data)
	}
	return Fragment{}, errors.New("invalid step state")
}

// ResolveSteps resolves and accumulates results for a sequence of steps.
func ResolveSteps(steps []Step, with map[string]string, data Data) (Fragment, error) {
	var frag Fragment
	for i, step := range steps {
		resolved, err := step.Resolve(with, data)
		if err != nil {
			return Fragment{}, err
		}
		if i == 0 {
			frag = resolved
		} else {
			frag = frag.Join(resolved)
		}
	}
	return frag, nil
}

// Fragment defines a concrete shell script with its system requirements.
type Fragment struct {
	Script string
	Needs  []string
}

func (r Fragment) Join(other Fragment) Fragment {
	var script string
	if (r.Script == "") || (other.Script == "") {
		script = cmp.Or(r.Script, other.Script)
	} else {
		script = strings.Join([]string{r.Script, other.Script}, "\n")
	}
	var needs []string
	seen := map[string]bool{}
	for _, need := range append(r.Needs, other.Needs...) {
		if _, ok := seen[need]; !ok {
			seen[need] = true
			needs = append(needs, need)
		}
	}
	return Fragment{
		Script: script,
		Needs:  needs,
	}
}
