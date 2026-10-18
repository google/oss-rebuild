// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// sourceRefRepo has two commits, the first tagged v1.0.0.
const sourceRefRepo = `
commits:
  - id: first
    branch: main
    tag: v1.0.0
    message: first
    files:
      rollup.config.js: "export default {}"
  - id: second
    parent: first
    branch: main
    message: second
    files:
      rollup.config.js: "export default {plugins: []}"
`

func TestSetSourceRef(t *testing.T) {
	repo, err := gitxtest.CreateRepoFromYAML(sourceRefRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	first, second := repo.Commits["first"].String(), repo.Commits["second"].String()
	for _, tc := range []struct {
		name    string
		ref     string
		reason  string
		want    string
		wantErr string
	}{
		{name: "NoReason", ref: "v1.0.0", wantErr: "reason"},
		{name: "UnknownRevision", ref: "deadbeef", reason: "because", wantErr: "not a revision"},
		{name: "SameRef", ref: second, reason: "because", wantErr: "already"},
		{name: "Tag", ref: "v1.0.0", reason: "the release tag names this commit", want: first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewDefaultAgent(rebuild.Target{Package: "fixture"}, &AgentDeps{})
			a.repo = repo.Repository
			a.loc = rebuild.Location{Repo: "https://example.com/repo", Ref: second}
			got, err := a.setSourceRef(context.Background(), tc.ref, tc.reason)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("setSourceRef() err = %v, want %q", err, tc.wantErr)
				}
				if a.refMoved || len(a.refMoves) != 0 || a.loc.Ref != second {
					t.Errorf("refused call moved the ref: %+v", a.refMoves)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("setSourceRef() = %q, %v; want %q", got, err, tc.want)
			}
			want := []refMove{{From: second, To: tc.want, Asked: tc.ref, Reason: tc.reason}}
			if diff := cmp.Diff(want, a.refMoves, cmpopts.IgnoreFields(refMove{}, "At")); diff != "" {
				t.Errorf("refMoves diff (-want +got):\n%s", diff)
			}
			if !a.refMoved {
				t.Error("refMoved = false after a move")
			}
			if _, ref := a.repoProvider(); ref != tc.want {
				t.Errorf("git tools ref = %s, want %s", ref, tc.want)
			}
		})
	}
}

// vmShell answers each command with the next result, or fails them all with err.
type vmShell struct {
	fakeShell
	results []CommandResult
	err     error
}

func (s *vmShell) RunCommand(_ context.Context, command string, _ int, _ schema.ScratchLabel) (CommandResult, error) {
	s.scripts = append(s.scripts, command)
	if s.err != nil {
		return CommandResult{}, s.err
	}
	res := s.results[0]
	s.results = s.results[1:]
	return res, nil
}

func TestResolveCommitOnVM(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shell   vmShell
		wantSHA string
		wantErr string
	}{
		{name: "Found", shell: vmShell{results: []CommandResult{{Output: "abc\n"}}}, wantSHA: "abc"},
		{name: "Fetched", shell: vmShell{results: []CommandResult{{ExitCode: 1}, {}, {Output: "def\n"}}}, wantSHA: "def"},
		{name: "NotARevision", shell: vmShell{results: []CommandResult{{ExitCode: 1}, {ExitCode: 128, Output: "fatal: couldn't find remote ref v1"}}}, wantErr: "is not a revision"},
		{name: "FetchFailed", shell: vmShell{results: []CommandResult{{ExitCode: 1}, {ExitCode: 128, Output: "fatal: unable to access the remote"}}}, wantErr: "Git said: exit 128: fatal: unable to access"},
		{name: "SeedRefusesFetch", shell: vmShell{results: []CommandResult{{ExitCode: 1}, {ExitCode: 255, Output: "error: cannot open '/seed/repo.git/FETCH_HEAD': Read-only file system"}}}, wantErr: "Git said: exit 255: error: cannot open"},
		{name: "NoContainer", shell: vmShell{results: []CommandResult{{ExitCode: 125, Output: "no build container found; run a build first"}}}, wantErr: "no build container"},
		{name: "ExecFailed", shell: vmShell{err: errors.New("exec failed")}, wantErr: "exec failed"},
		{name: "OutputLost", shell: vmShell{results: []CommandResult{{}}}, wantErr: "no commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewDefaultAgent(rebuild.Target{Package: "p"}, &AgentDeps{Inference: &inferenceservice.Router{}, Shell: &tc.shell})
			sha, err := a.resolveCommit(context.Background(), "v1")
			if sha != tc.wantSHA || (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("resolveCommit() = %q, %v, want %q, %q", sha, err, tc.wantSHA, tc.wantErr)
			}
		})
	}
}
