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
