// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"
	"time"
)

func TestGroupDigits(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{14889, "14,889"},
		{1234567, "1,234,567"},
		{-123, "-123"},
		{-1234, "-1,234"},
	} {
		if got := groupDigits(tc.n); got != tc.want {
			t.Errorf("groupDigits(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestSince(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"Seconds", 59 * time.Second, "just now"},
		{"Future", -time.Hour, "just now"},
		{"Minutes", 61 * time.Second, "1m ago"},
		{"Hours", 23*time.Hour + 59*time.Minute, "23h ago"},
		{"Days", 8*24*time.Hour + time.Hour, "8d ago"},
		{"PastAMonth", 30 * 24 * time.Hour, "2026-09-06"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := since(now, now.Add(-tc.ago)); got != tc.want {
				t.Errorf("since() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDay(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if got := day(now, time.Date(2026, 8, 20, 23, 0, 0, 0, time.UTC)); got != "Aug 20" {
		t.Errorf("day() = %q, want %q", got, "Aug 20")
	}
	if got := day(now, time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)); got != "Dec 31, 2025" {
		t.Errorf("day() = %q, want %q", got, "Dec 31, 2025")
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "0 rebuilds", 1: "1 rebuild", 22: "22 rebuilds"} {
		if got := plural(n, "rebuild"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFailure(t *testing.T) {
	for _, tc := range []struct {
		name, msg, stage, cause string
	}{
		{"Inference", "getting strategy: fetching inference: rpc error: code = FailedPrecondition desc = failed to infer strategy: no git repo found", "infer", "no git repo found"},
		{"Build", "executing rebuild: executing rebuild: build failed in container run phase; the failing command was `python3 -m build --wheel -n`", "build", "build failed in container run phase; the failing command was `python3 -m build --wheel -n`"},
		{"Timeout", "executing rebuild: waiting for build: context deadline exceeded", "build", "waiting for build: context deadline exceeded"},
		{"Mismatch", "executing rebuild: rpc error: code = FailedPrecondition desc = rebuild content mismatch", "compare", "rebuild content mismatch"},
		{"Upstream", "executing rebuild: comparing artifacts: fetching upstream artifact: 429 Too Many Requests", "compare", "comparing artifacts: fetching upstream artifact: 429 Too Many Requests"},
		{"Session", "Inference failed: generating strategy: AI-assisted inference failed: AI-assisted inference failed: rpc error: code = Internal desc = failed to infer strategy: clone failed [repo=https://0xacab.org/x]: Unsupported repo URI", "", "clone failed [repo=https://0xacab.org/x]: Unsupported repo URI"},
		{"Unwrapped", "Session throttled: 5 transient LLM failures", "", "Session throttled: 5 transient LLM failures"},
		{"HTMLAfterWrapper", "getting strategy: fetching inference: \n<html><body>502</body></html>", "infer", "HTTP error response"},
		{"HTMLAfterGRPCFraming", "executing rebuild: rpc error: code = Unavailable desc = <!DOCTYPE html><html>502</html>", "build", "HTTP error response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedStage(tc.msg); got != tc.stage {
				t.Errorf("failedStage() = %q, want %q", got, tc.stage)
			}
			if got := failureCause(tc.msg); got != tc.cause {
				t.Errorf("failureCause() = %q, want %q", got, tc.cause)
			}
		})
	}
}

func TestSimplifyMessage(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			"LongGoogleAPIError",
			"outer step: inner failure: googleapi: Error 429: foo bar baz\nDetails:\n[{\"k\": \"v\"}]\n, qux",
			"outer step: inner failure: googleapi: Error 429",
		},
		{
			"LongGoogleAPIHTMLError",
			"outer step: inner failure: googleapi: got HTTP response code 404 with body: <!DOCTYPE html><html>...</html>",
			"outer step: inner failure: googleapi: got HTTP response code 404",
		},
		{
			"CascadingOperationError",
			"outer step: rpc error: code = ResourceExhausted desc = inner failure: op operation-0000-xyz failed: FOO_BAR: baz qux",
			"outer step: rpc error: code = ResourceExhausted desc = inner failure",
		},
		{
			"HTMLWrapperError",
			"outer step: inner failure: \n<html><head><title>500</title></head><body>foo</body></html>\n: 500 Internal Server Error: bar",
			"outer step: inner failure: HTTP error response",
		},
		{
			"BareHTMLError",
			"<!DOCTYPE html><html><body>foo bar</body></html>",
			"HTTP error response",
		},
		{
			"Upstream429Preserved",
			"comparing artifacts: fetching upstream artifact: 429 Too Many Requests",
			"comparing artifacts: fetching upstream artifact: 429 Too Many Requests",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := simplifyMessage(tc.in); got != tc.want {
				t.Errorf("simplifyMessage() =\n  %q\nwant:\n  %q", got, tc.want)
			}
		})
	}
}

func TestArtifactVariant(t *testing.T) {
	for _, tc := range []struct {
		artifact, version, want string
	}{
		{"mongrel2-0.2.4.gem", "0.2.4", ""},
		{"docutils-0.19.tar.gz", "0.19", ""},
		{"django_appdata-0.1.6-py2-none-any.whl", "0.1.6", "py2-none-any"},
		{"iptools-0.6.1-py2.py3-none-any.whl", "0.6.1", "py2.py3-none-any"},
		{"hutool-extra-5.8.38-sources.jar", "5.8.38", "sources"},
		{"foo_1.0-1_amd64.deb", "1.0-2", "foo_1.0-1_amd64.deb"},
	} {
		if got := artifactVariant(tc.artifact, tc.version); got != tc.want {
			t.Errorf("artifactVariant(%q, %q) = %q, want %q", tc.artifact, tc.version, got, tc.want)
		}
	}
}
