// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package buildpolicy

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	in := ReviewInput{Deps: "npm ci", Build: "cat << 'EOF' > Cargo.lock\nx\nEOF\nnpm pack"}
	lock := Violation{Rule: RuleTranscribe, Line: "cat << 'EOF' > Cargo.lock", Why: "lockfile typed in"}
	for _, tc := range []struct {
		name    string
		review  Review
		verdict string
		nviol   int
	}{
		{"reject with quoted line", Review{Verdict: VerdictReject, Violations: []Violation{lock}}, VerdictReject, 1},
		// A violation quoting a line not in the script is dropped, and the
		// verdict follows what is left.
		{"unquotable line dropped", Review{Verdict: VerdictReject, Violations: []Violation{{Rule: RuleTranscribe, Line: "cat << 'EOF' > package-lock.json"}}}, VerdictAllow, 0},
		{"unquotable line dropped, declaration left", Review{Verdict: VerdictReject, Violations: []Violation{{Rule: RuleTranscribe, Line: "cat << 'EOF' > package-lock.json"}}, Declarations: []Declaration{{}}}, VerdictDeclare, 0},
		{"blank line dropped", Review{Verdict: VerdictReject, Violations: []Violation{{Rule: RuleTranscribe, Line: "  "}}}, VerdictAllow, 0},
		// A surviving violation makes the verdict reject whatever was said.
		{"violation forces reject", Review{Verdict: VerdictAllow, Violations: []Violation{lock}}, VerdictReject, 1},
		{"declare without declarations", Review{Verdict: VerdictDeclare}, VerdictAllow, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := normalize(&tc.review, in); r.Verdict != tc.verdict || len(r.Violations) != tc.nviol {
				t.Errorf("normalize = %+v, want verdict %s with %d violations", r, tc.verdict, tc.nviol)
			}
		})
	}
}

func TestReviewMessage(t *testing.T) {
	r := &Review{Verdict: VerdictReject, Violations: []Violation{{Rule: RuleCopy, Line: "cp /upstream/x.whl dist/", Why: "copies the artifact"}}, Rationale: "supplied the answer"}
	msg := r.Message()
	for _, want := range []string{"proposal refused", "F2 taking content from the published artifact", "cp /upstream/x.whl dist/", "supplied the answer", "not counted as a build"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Message lacks %q:\n%s", want, msg)
		}
	}
}

func TestPrompt(t *testing.T) {
	p := Prompt(ReviewInput{Target: Target{Package: "p", Version: "1", Ref: "abc"}, Ecosystem: "npm", Artifact: "p-1.tgz", Deps: "D", Build: "B", PreviousBuild: "PB", PreviousError: "rebuild content mismatch"})
	for _, want := range []string{"npm p 1 (p-1.tgz), source ref abc", "PB", "rebuild content mismatch", "Proposed deps phase:\n```bash\nD\n```", "Proposed build phase:\n```bash\nB\n```"} {
		if !strings.Contains(p, want) {
			t.Errorf("Prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestTranscriptReview(t *testing.T) {
	in := ReviewInput{Deps: "D", Build: "npm pack", Transcript: "cp /tmp/up/package/legacy.js /src/legacy.js\nnpm pack"}
	p := Prompt(in)
	for _, want := range []string{"judge the transcript as part of the proposal", "```bash\ncp /tmp/up/package/legacy.js /src/legacy.js\nnpm pack\n```"} {
		if !strings.Contains(p, want) {
			t.Errorf("Prompt lacks %q:\n%s", want, p)
		}
	}
	// A line quoted from the transcript is a violation the proposal contains.
	r := normalize(&Review{Verdict: VerdictReject, Violations: []Violation{{Rule: RuleCopy, Line: "cp /tmp/up/package/legacy.js /src/legacy.js", Why: "the file came out of the upstream archive"}}}, in)
	if r.Verdict != VerdictReject || len(r.Violations) != 1 {
		t.Errorf("normalize = %+v, want the transcript's line kept", r)
	}
}
