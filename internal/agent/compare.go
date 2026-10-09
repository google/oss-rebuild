// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/pkg/errors"
)

// comparison is a candidate artifact compared with the staged upstream.
type comparison struct {
	match                       bool
	candidate, upstream         string // the stabilized forms' sha256
	candidateForm, upstreamForm string // the stabilized forms' paths
	summary                     string // diffr's file-level summary, empty without file-level differences
}

// compareCandidate compares the candidate at p, a host path or a path under
// /src in the retained build container, with the staged upstream.
func compareCandidate(ctx context.Context, r *ScratchRunner, p string) (comparison, error) {
	candidate, err := candidatePath(r, p)
	if err != nil {
		return comparison{}, err
	}
	code, out, err := r.CheckArtifact(ctx, candidate)
	if err != nil {
		return comparison{}, err
	}
	text := string(out)
	if code == exitDiffSetupFailed {
		return comparison{}, errors.New(strings.TrimSpace(text))
	}
	first, summary, _ := strings.Cut(text, "\n")
	f := strings.Fields(first)
	if len(f) != 4 {
		return comparison{}, errors.Errorf("the comparison exited %d: %s", code, strings.TrimSpace(headLimit(text, 2000)))
	}
	c := comparison{candidate: f[0], upstream: f[1], candidateForm: f[2], upstreamForm: f[3]}
	switch code {
	case 0:
		c.match = c.candidate == c.upstream
	case 1:
		c.summary = summary
	default:
		return comparison{}, errors.Errorf("diffr exited %d: %s", code, strings.TrimSpace(summary))
	}
	return c, nil
}

// candidatePath maps p to the host: /src onto the retained build's tree, a
// relative path under the work dir.
func candidatePath(r *ScratchRunner, p string) (string, error) {
	workDir := cmp.Or(r.WorkDir, scratch.DefaultWorkDir)
	switch {
	case p == "/src" || strings.HasPrefix(p, "/src/"):
		id := r.LastRunID()
		if id == "" {
			return "", errors.Errorf("%s: no retained build to map it onto", p)
		}
		p = scratch.SourcePath(workDir, id) + strings.TrimPrefix(p, "/src")
	case !path.IsAbs(p):
		p = path.Join(workDir, p)
	}
	p = path.Clean(p)
	if !shellWord.MatchString(p) {
		return "", errors.Errorf("%q is not a plain path", p)
	}
	return p, nil
}

var summaryGroupRE = regexp.MustCompile(`^\s*(?:differ|only in .+) \((\d+)\):$`)

// summaryHead keeps the first max lines of a diffr summary and counts the
// entries its group headers report.
func summaryHead(summary string, max int) (text string, lines, shown, entries int) {
	all := strings.Split(strings.TrimSuffix(summary, "\n"), "\n")
	for _, l := range all {
		if m := summaryGroupRE.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			entries += n
		}
	}
	shown = min(len(all), max)
	return strings.Join(all[:shown], "\n"), len(all), shown, entries
}
