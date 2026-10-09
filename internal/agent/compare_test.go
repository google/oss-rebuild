// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcsx/gcsxtest"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestCompareCandidate(t *testing.T) {
	const forms = " /home/builder/checks/x.tgz.stabilized /home/builder/upstream/x.tgz.stabilized\n"
	for _, tc := range []struct {
		name    string
		code    int
		out     string
		want    comparison
		wantErr string
	}{
		{name: "Match", out: "ab ab" + forms, want: comparison{match: true, candidate: "ab", upstream: "ab"}},
		{name: "BytesDiffer", out: "ab cd" + forms, want: comparison{candidate: "ab", upstream: "cd"}},
		{name: "FilesDiffer", code: 1, out: "ab cd" + forms + "  differ (1):\n    a\n", want: comparison{candidate: "ab", upstream: "cd", summary: "  differ (1):\n    a\n"}},
		{name: "SetupFailed", code: exitDiffSetupFailed, out: "/home/builder/x.tgz: no such file on the VM host\n", wantErr: "no such file"},
		{name: "UnreadableOutput", code: 1, out: "garbage\n", wantErr: "the comparison exited 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRunner(&fakeExecutor{})
			r.GCSClient = gcsxtest.Client(t, map[string]string{"out/check": tc.out})
			r.Stubs = scratch.Stubs{
				ExecCreate: func(context.Context, schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
					return &longrunning.Operation[schema.ScratchExecResult]{ID: "check", Done: true, Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: tc.code, OutURI: "gs://out/check"}}, nil
				},
			}
			got, err := compareCandidate(context.Background(), r, "/home/builder/x.tgz")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if tc.want.candidate != "" {
				tc.want.candidateForm, tc.want.upstreamForm = "/home/builder/checks/x.tgz.stabilized", "/home/builder/upstream/x.tgz.stabilized"
			}
			if err != nil {
				t.Fatalf("compareCandidate() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(comparison{})); diff != "" {
				t.Errorf("compareCandidate() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCandidatePath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		built   bool
		in      string
		want    string
		wantErr string
	}{
		{name: "NoRetainedBuild", in: "/src/x.tgz", wantErr: "no retained build"},
		{name: "ContainerFile", built: true, in: "/src/x.tgz", want: "/home/builder/builds/iter-2/src/x.tgz"},
		{name: "ContainerRoot", built: true, in: "/src", want: "/home/builder/builds/iter-2/src"},
		{name: "EarlierBuild", built: true, in: "builds/iter-1/src/./x.tgz", want: "/home/builder/builds/iter-1/src/x.tgz"},
		{name: "HostPath", built: true, in: "/home/builder/upstream/x.tgz", want: "/home/builder/upstream/x.tgz"},
		{name: "ShellCharacters", built: true, in: "/src/x;rm -rf /", wantErr: "not a plain path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := testRunner(&fakeExecutor{result: build.Result{Error: &scratch.ExitError{Code: 1}}})
			if tc.built {
				runner.Run(context.Background(), "iter-2", testStrategy())
			}
			got, err := candidatePath(runner, tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("candidatePath(%q) err = %v, want %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("candidatePath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestSummaryHead(t *testing.T) {
	summary := "within candidate (vs upstream):\n  differ (2):\n    a\n    b\n  only in upstream (1):\n    c\n"
	text, lines, shown, entries := summaryHead(summary, 3)
	if text != "within candidate (vs upstream):\n  differ (2):\n    a" || lines != 6 || shown != 3 || entries != 3 {
		t.Errorf("summaryHead = %q, %d, %d, %d", text, lines, shown, entries)
	}
}
