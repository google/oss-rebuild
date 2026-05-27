// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package annotateinferencediff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

const (
	headerDiffers = "# inference differs:"
	headerFails   = "# inference fails:"
)

// diff is one disagreement between a manual recording and the inferred
// strategy. When multiline is false, body is rendered as a single
// `#   field: body` line. When true, body is a unified-diff block (no @@
// header, one " "/"-"/"+"-prefixed line per row) and renders as
// `#  field:` followed by indented body lines.
type diff struct {
	field     string
	body      string
	multiline bool
}

func toWorkflowStrategy(s rebuild.Strategy) (*rebuild.WorkflowStrategy, bool) {
	if wf, ok := s.(*rebuild.WorkflowStrategy); ok {
		return wf, true
	} else if f, ok := s.(rebuild.Flowable); ok {
		return f.ToWorkflow(), true
	} else {
		return nil, false
	}
}

func diffStrategies(man, inf rebuild.Strategy) ([]diff, error) {
	// Same typed strategy (not a workflow): structural diff at typed-field
	// granularity preserves nice framing like `requirements: removed X`.
	if reflect.TypeOf(man) == reflect.TypeOf(inf) {
		if _, isWF := man.(*rebuild.WorkflowStrategy); !isWF {
			mm, err := asJSONMap(man)
			if err != nil {
				return nil, err
			}
			im, err := asJSONMap(inf)
			if err != nil {
				return nil, err
			}
			return diffMaps("", mm, im), nil
		}
	}
	// Otherwise, bring both to WorkflowStrategy, if possible.
	manWF, ok1 := toWorkflowStrategy(man)
	infWF, ok2 := toWorkflowStrategy(inf)
	if !ok1 || !ok2 {
		return []diff{{
			field: "strategy_type",
			body:  fmt.Sprintf("%T (inferred: %T)", man, inf),
		}}, nil
	}
	// Diff top-level non-flow fields structurally (location, output_dir,
	// requires, ...) and emit per-phase unified diffs over resolved scripts.
	mm, err := asJSONMap(manWF)
	if err != nil {
		return nil, err
	}
	im, err := asJSONMap(infWF)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"src", "deps", "build"} {
		delete(mm, k)
		delete(im, k)
	}
	out := diffMaps("", mm, im)
	phaseDiffs, err := renderedPhaseDiffs(manWF, infWF)
	if err != nil {
		return nil, err
	}
	return append(out, phaseDiffs...), nil
}

func diffMaps(prefix string, a, b map[string]any) []diff {
	var out []diff
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, k := range sortedKeys {
		va, ina := a[k]
		vb, inb := b[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch {
		case !ina:
			out = append(out, diff{field: path, body: fmt.Sprintf("(unset, inferred: %s)", formatScalar(vb))})
			continue
		case !inb:
			out = append(out, diff{field: path, body: fmt.Sprintf("%s (inferred: unset)", formatScalar(va))})
			continue
		case reflect.DeepEqual(va, vb):
			continue
		}
		if sa, ok := va.(map[string]any); ok {
			if sb, ok := vb.(map[string]any); ok {
				out = append(out, diffMaps(path, sa, sb)...)
				continue
			}
		}
		if la, ok := asSlice[string](va); ok {
			if lb, ok := asSlice[string](vb); ok {
				out = append(out, diff{field: path, body: setDiff(la, lb)})
				continue
			}
		}
		out = append(out, diff{field: path, body: fmt.Sprintf("%s (inferred: %s)", formatScalar(va), formatScalar(vb))})
	}
	return out
}

func setDiff[T comparable](man, inf []T) string {
	manSet := map[T]bool{}
	for _, r := range man {
		manSet[r] = true
	}
	infSet := map[T]bool{}
	for _, r := range inf {
		infSet[r] = true
	}
	var added, removed []string
	for _, r := range man {
		if !infSet[r] {
			added = append(added, fmt.Sprint(r))
		}
	}
	for _, r := range inf {
		if !manSet[r] {
			removed = append(removed, fmt.Sprint(r))
		}
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	return strings.Join(parts, "; ") + " (vs inferred)"
}

// renderedPhaseDiffs resolves two workflows to concrete shell scripts and
// emits one unified diff entry per differing phase.
// NOTE: The target and env used contain dummy values so output will be
// representative but not exact.
func renderedPhaseDiffs(man, inf *rebuild.WorkflowStrategy) ([]diff, error) {
	target := rebuild.Target{Ecosystem: "any", Package: "pkg", Version: "ver", Artifact: "file"}
	env := rebuild.BuildEnv{TimewarpHost: "TIMEWARP"}
	manInst, err := man.GenerateFor(target, env)
	if err != nil {
		return nil, fmt.Errorf("rendering manual: %w", err)
	}
	infInst, err := inf.GenerateFor(target, env)
	if err != nil {
		return nil, fmt.Errorf("rendering inferred: %w", err)
	}
	var out []diff
	for _, ph := range []struct{ name, man, inf string }{
		{"source", manInst.Source, infInst.Source},
		{"deps", manInst.Deps, infInst.Deps},
		{"build", manInst.Build, infInst.Build},
	} {
		body, err := phaseDiffBody(ph.inf, ph.man)
		if err != nil {
			return nil, fmt.Errorf("phase %s: %w", ph.name, err)
		}
		if body == "" {
			continue
		}
		out = append(out, diff{field: ph.name + " diff", body: body, multiline: true})
	}
	return out, nil
}

// phaseDiffBody returns a unified diff between two phase scripts, stripping the hunk header.
func phaseDiffBody(inferred, manual string) (string, error) {
	inferred = normalizePipInstalls(inferred)
	manual = normalizePipInstalls(manual)
	// Equalise trailing newlines so identical last lines don't show as
	// removed+added under the diff library's no-newline-at-eof handling.
	if !strings.HasSuffix(inferred, "\n") {
		inferred += "\n"
	}
	if !strings.HasSuffix(manual, "\n") {
		manual += "\n"
	}
	if inferred == manual {
		return "", nil
	}
	d, err := gitdiff.Strings(inferred, manual)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for ln := range strings.SplitSeq(strings.TrimRight(d, "\n"), "\n") {
		if strings.HasPrefix(ln, "@@") {
			continue
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// normalizePipInstalls collapses a run of consecutive simple `pip install`
// lines (no flags) into a single sorted line, on either side of the diff.
// pypi/install-deps emits one `pip install` per requirement, which produces
// noisy line-by-line diffs otherwise.
//
// TODO: Fix at the source by making pypi/install-deps emit a single command.
// The per-line emission is currently load-bearing for overriding existing
// requirements when they disagree with the observed build toolchain.
func normalizePipInstalls(script string) string {
	lines := strings.Split(script, "\n")
	var out []string
	var blockPkgs []string
	var blockPrefix string

	flushBlock := func() {
		if blockPrefix == "" {
			return
		}
		seen := map[string]bool{}
		uniq := blockPkgs[:0]
		for _, p := range blockPkgs {
			if !seen[p] {
				seen[p] = true
				uniq = append(uniq, p)
			}
		}
		sort.Strings(uniq)
		out = append(out, blockPrefix+strings.Join(uniq, " "))
		blockPkgs = nil
		blockPrefix = ""
	}

	for _, ln := range lines {
		prefix, args, ok := splitPipInstall(ln)
		if !ok {
			flushBlock()
			out = append(out, ln)
			continue
		}
		pkgs := parsePipArgs(args)
		if pkgs == nil {
			flushBlock()
			out = append(out, ln)
			continue
		}
		if blockPrefix != "" && blockPrefix != prefix {
			flushBlock()
		}
		blockPrefix = prefix
		blockPkgs = append(blockPkgs, pkgs...)
	}
	flushBlock()
	return strings.Join(out, "\n")
}

// splitPipInstall matches a line of the form `<path>pip install <args>` where
// <path> is a whitespace-free token (possibly empty). Returns prefix as
// `<path>pip install ` (trailing space). Rejects lines where `<path>` itself
// contains whitespace (e.g. `cd /src && pip install ...`).
func splitPipInstall(ln string) (prefix, args string, ok bool) {
	const marker = "pip install "
	idx := strings.Index(ln, marker)
	if idx == -1 {
		return "", "", false
	}
	if strings.ContainsAny(ln[:idx], " \t") {
		return "", "", false
	}
	return ln[:idx+len(marker)], ln[idx+len(marker):], true
}

// parsePipArgs tokenises pip install args, respecting single-quoted spans.
// Returns nil if the args start with a `-` (a flag) or are empty.
func parsePipArgs(args string) []string {
	args = strings.TrimSpace(args)
	if args == "" || strings.HasPrefix(args, "-") {
		return nil
	}
	var pkgs []string
	var cur strings.Builder
	inQuote := false
	for _, r := range args {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				pkgs = append(pkgs, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		pkgs = append(pkgs, cur.String())
	}
	return pkgs
}

// stripExistingHeader removes the `# inference {differs,fails}:` indented block.
func stripExistingHeader(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	// blockEnd: first line that does NOT start with `#` (i.e., end of the
	// leading comment block). All `#` lines → len(lines).
	blockEnd := slices.IndexFunc(lines, func(s string) bool { return !strings.HasPrefix(s, "#") })
	if blockEnd < 0 {
		blockEnd = len(lines)
	}
	// headerIdx: position of the inference header within that block, if any.
	headerIdx := slices.IndexFunc(lines[:blockEnd], func(s string) bool {
		return strings.HasPrefix(s, headerDiffers) || strings.HasPrefix(s, headerFails)
	})
	if headerIdx >= 0 {
		end := headerIdx + 1
		for end < blockEnd && strings.HasPrefix(lines[end], "#  ") {
			end++
		}
		lines = append(lines[:headerIdx], lines[end:]...)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

func formatDiffersHeader(diffs []diff) string {
	var b strings.Builder
	b.WriteString(headerDiffers + "\n")
	for _, d := range diffs {
		if d.multiline {
			fmt.Fprintf(&b, "#  %s:\n", d.field)
			for line := range strings.SplitSeq(strings.TrimRight(d.body, "\n"), "\n") {
				fmt.Fprintf(&b, "#   %s\n", line)
			}
		} else {
			fmt.Fprintf(&b, "#   %s: %s\n", d.field, d.body)
		}
	}
	return b.String()
}

// formatFailsHeader emits `# inference fails: <reason>` where reason can be multi-line.
// In the multi-line case, each additional line is indented with `#  ` indented
// prefix to ensure it falls inside what we consider to be the header.
func formatFailsHeader(reason string) string {
	reason = strings.TrimRight(reason, "\n")
	if !strings.Contains(reason, "\n") {
		return fmt.Sprintf("%s %s\n", headerFails, reason)
	}
	var b strings.Builder
	b.WriteString(headerFails + "\n")
	for _, line := range strings.Split(reason, "\n") {
		fmt.Fprintf(&b, "#  %s\n", line)
	}
	return b.String()
}

func formatScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "(unset)"
	case string:
		if x == "" {
			return `""`
		}
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// asJSONMap normalizes a value via JSON round-trip into a map.
func asJSONMap(s any) (map[string]any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// asSlice returns (slice, true) if v is a JSON-roundtripped array whose
// elements are all of type T.
func asSlice[T any](v any) ([]T, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]T, 0, len(arr))
	for _, e := range arr {
		t, ok := e.(T)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}
