// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"bytes"
	"strings"
	"testing"
	"time"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func makeAction(id, parentID int64, argv []string, isFork bool, startSec int64) *sgpb.Action {
	a := &sgpb.Action{}
	a.SetId(id)
	if parentID != 0 {
		a.SetParentActionId(parentID)
	}
	a.SetIsFork(isFork)
	ei := &sgpb.ExecInfo{}
	ei.SetArgv(argv)
	a.SetExecInfo(ei)
	a.SetStartTime(timestamppb.New(time.Unix(startSec, 0)))
	a.SetEndTime(timestamppb.New(time.Unix(startSec+1, 0)))
	return a
}

func TestRenderTree_Basic(t *testing.T) {
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh", "-c", "build.sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(2, 1, []string{"/usr/bin/make", "-j4"}, false, 101),
				execPath: "/usr/bin/make",
				children: []*treeNode{
					{
						action:   makeAction(3, 2, []string{"/usr/bin/gcc", "-c", "foo.c"}, false, 102),
						execPath: "/usr/bin/gcc",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{})
	got := buf.String()

	want := []string{
		"+ /bin/sh -c build.sh",
		"++ /usr/bin/make -j4",
		"+++ /usr/bin/gcc -c foo.c",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\ngot:\n%s", w, got)
		}
	}
}

func TestRenderTree_ForkCollapsing(t *testing.T) {
	// Fork node should be skipped, children promoted.
	fork := &treeNode{
		action:   makeAction(2, 1, []string{"/bin/sh"}, true, 101),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(3, 2, []string{"/usr/bin/gcc", "-c", "foo.c"}, false, 102),
				execPath: "/usr/bin/gcc",
			},
		},
	}
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh", "build.sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{fork},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{})
	got := buf.String()

	// gcc should be at depth 2 (promoted past the fork).
	if !strings.Contains(got, "++ /usr/bin/gcc -c foo.c") {
		t.Errorf("fork not collapsed, got:\n%s", got)
	}
	// The fork node itself should not appear.
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.Contains(line, "fork") {
			t.Errorf("fork node should not appear, got line: %s", line)
		}
	}
}

func TestRenderTree_ShowForks(t *testing.T) {
	fork := &treeNode{
		action:   makeAction(2, 1, []string{"/bin/sh"}, true, 101),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(3, 2, []string{"/usr/bin/gcc"}, false, 102),
				execPath: "/usr/bin/gcc",
			},
		},
	}
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh", "build.sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{fork},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{ShowForks: true})
	got := buf.String()

	if !strings.Contains(got, "++ /bin/sh  [fork]") {
		t.Errorf("fork node should appear with --show-forks, got:\n%s", got)
	}
}

func TestRenderTree_SiblingCollapsing(t *testing.T) {
	children := make([]*treeNode, 15)
	for i := range children {
		children[i] = &treeNode{
			action:   makeAction(int64(i+2), 1, []string{"/usr/bin/cc", "-c", "file.c"}, false, int64(100+i)),
			execPath: "/usr/bin/cc",
		}
	}
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/make"}, false, 99),
		execPath: "/bin/make",
		children: children,
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{Collapse: 10})
	got := buf.String()

	if !strings.Contains(got, "[15x]") {
		t.Errorf("expected sibling collapse with [15x], got:\n%s", got)
	}
	// Should have exactly 2 lines: root + collapsed.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d:\n%s", len(lines), got)
	}
}

func TestRenderTree_MaxDepth(t *testing.T) {
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(2, 1, []string{"/usr/bin/make"}, false, 101),
				execPath: "/usr/bin/make",
				children: []*treeNode{
					{
						action:   makeAction(3, 2, []string{"/usr/bin/gcc"}, false, 102),
						execPath: "/usr/bin/gcc",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{MaxDepth: 2})
	got := buf.String()

	if strings.Contains(got, "gcc") {
		t.Errorf("depth 3 node should be hidden with max-depth=2, got:\n%s", got)
	}
	if !strings.Contains(got, "make") {
		t.Errorf("depth 2 node should be visible, got:\n%s", got)
	}
}

func TestRenderTree_DockerAnnotation(t *testing.T) {
	a := makeAction(1, 0, []string{"/bin/sh"}, false, 100)
	a.SetMetadata(map[string]string{"docker": "abcdef1234567890abcdef"})
	root := &treeNode{
		action:   a,
		execPath: "/bin/sh",
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{})
	got := buf.String()

	if !strings.Contains(got, "[container:abcdef123456]") {
		t.Errorf("expected docker annotation, got:\n%s", got)
	}
}

func TestRenderTree_ShowIDs(t *testing.T) {
	root := &treeNode{
		action:   makeAction(42, 0, []string{"/bin/sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(99, 42, []string{"/usr/bin/make"}, false, 101),
				execPath: "/usr/bin/make",
			},
		},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{ShowIDs: true})
	got := buf.String()

	if !strings.Contains(got, "[id=42]") {
		t.Errorf("expected id=42 annotation, got:\n%s", got)
	}
	if !strings.Contains(got, "[id=99]") {
		t.Errorf("expected id=99 annotation, got:\n%s", got)
	}
}

func TestRenderTree_Duration(t *testing.T) {
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh"}, false, 100),
		execPath: "/bin/sh",
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{ShowDuration: true})
	got := buf.String()

	if !strings.Contains(got, "1s") {
		t.Errorf("expected duration annotation, got:\n%s", got)
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abcdef1234567890", "abcdef123456"},
		{"short", "short"},
		{"exactly12ch", "exactly12ch"},
	}
	for _, tt := range tests {
		if got := shortID(tt.in); got != tt.want {
			t.Errorf("shortID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{65 * time.Second, "1m5s"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestRenderTree_AncestorID(t *testing.T) {
	// Build a tree: sh -> (make -> gcc, ls)
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh"}, false, 100),
		execPath: "/bin/sh",
		children: []*treeNode{
			{
				action:   makeAction(2, 1, []string{"/usr/bin/make"}, false, 101),
				execPath: "/usr/bin/make",
				children: []*treeNode{
					{
						action:   makeAction(3, 2, []string{"/usr/bin/gcc", "-c", "foo.c"}, false, 102),
						execPath: "/usr/bin/gcc",
					},
				},
			},
			{
				action:   makeAction(4, 1, []string{"/bin/ls"}, false, 103),
				execPath: "/bin/ls",
			},
		},
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{AncestorID: 3})
	got := buf.String()

	// Should show sh -> make -> gcc, but not ls.
	if !strings.Contains(got, "/bin/sh") {
		t.Errorf("expected /bin/sh in ancestor path, got:\n%s", got)
	}
	if !strings.Contains(got, "/usr/bin/make") {
		t.Errorf("expected /usr/bin/make in ancestor path, got:\n%s", got)
	}
	if !strings.Contains(got, "/usr/bin/gcc") {
		t.Errorf("expected /usr/bin/gcc in ancestor path, got:\n%s", got)
	}
	if strings.Contains(got, "/bin/ls") {
		t.Errorf("unexpected /bin/ls in ancestor path, got:\n%s", got)
	}

	// Each line should be at a different depth.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d:\n%s", len(lines), got)
	}
}

func TestRenderTree_AncestorID_NotFound(t *testing.T) {
	root := &treeNode{
		action:   makeAction(1, 0, []string{"/bin/sh"}, false, 100),
		execPath: "/bin/sh",
	}

	var buf bytes.Buffer
	renderTree(&buf, []*treeNode{root}, renderOpts{AncestorID: 999})
	got := buf.String()

	if got != "" {
		t.Errorf("expected empty output for missing ID, got:\n%s", got)
	}
}
