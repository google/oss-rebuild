// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgdiff

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
)

func TestDiff_EmptyGraphs(t *testing.T) {
	ctx := context.Background()

	old := &inmemory.SysGraph{
		GraphPb:     sgpb.SysGraph_builder{Id: proto.String("old")}.Build(),
		Actions:     make(map[int64]*sgpb.Action),
		ResourceMap: make(map[pbdigest.Digest]*sgpb.Resource),
	}
	new := &inmemory.SysGraph{
		GraphPb:     sgpb.SysGraph_builder{Id: proto.String("new")}.Build(),
		Actions:     make(map[int64]*sgpb.Action),
		ResourceMap: make(map[pbdigest.Digest]*sgpb.Resource),
	}

	diff, err := Diff(ctx, old, new, DefaultOptions())
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if diff.HasChanges() {
		t.Errorf("Expected no changes for empty graphs, got: %s", diff.Summary())
	}
}

func TestDiff_NewExecutable(t *testing.T) {
	ctx := context.Background()

	execDigest := pbdigest.NewFromBlob([]byte("curl binary"))

	old := &inmemory.SysGraph{
		GraphPb:     sgpb.SysGraph_builder{Id: proto.String("old")}.Build(),
		Actions:     make(map[int64]*sgpb.Action),
		ResourceMap: make(map[pbdigest.Digest]*sgpb.Resource),
	}

	new := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{Id: proto.String("new")}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id:                       proto.Int64(1),
				ExecutableResourceDigest: proto.String(execDigest.String()),
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"curl", "-sSL", "https://example.com"},
				}.Build(),
			}.Build(),
		},
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			execDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
				FileInfo: sgpb.FileInfo_builder{
					Path:   proto.String("/usr/bin/curl"),
					Digest: proto.String(execDigest.String()),
					Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
				}.Build(),
			}.Build(),
		},
	}

	diff, err := Diff(ctx, old, new, DefaultOptions())
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(diff.Executables.Added) != 1 {
		t.Errorf("Expected 1 added executable, got %d", len(diff.Executables.Added))
	}

	if len(diff.Executables.Added) > 0 && diff.Executables.Added[0].Path != "/usr/bin/curl" {
		t.Errorf("Expected path /usr/bin/curl, got %s", diff.Executables.Added[0].Path)
	}
}

func TestDiff_NewNetworkConnection(t *testing.T) {
	ctx := context.Background()

	netDigest := pbdigest.NewFromBlob([]byte("network address"))

	old := &inmemory.SysGraph{
		GraphPb:     sgpb.SysGraph_builder{Id: proto.String("old")}.Build(),
		Actions:     make(map[int64]*sgpb.Action),
		ResourceMap: make(map[pbdigest.Digest]*sgpb.Resource),
	}

	new := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{Id: proto.String("new")}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Outputs: map[string]*sgpb.ResourceInteractions{
					netDigest.String(): sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{}.Build(),
						},
					}.Build(),
				},
			}.Build(),
		},
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			netDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
				NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
					Protocol: proto.String("tcp"),
					Address:  proto.String("example.com:443"),
				}.Build(),
			}.Build(),
		},
	}

	diff, err := Diff(ctx, old, new, DefaultOptions())
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(diff.Network.Added) != 1 {
		t.Errorf("Expected 1 added network connection, got %d", len(diff.Network.Added))
	}

	if len(diff.Network.Added) > 0 && diff.Network.Added[0].Address != "example.com:443" {
		t.Errorf("Expected address example.com:443, got %s", diff.Network.Added[0].Address)
	}

	// Should generate a security alert for new network connection.
	found := false
	for _, alert := range diff.SecurityAlerts {
		if alert.Category == "network" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected security alert for new network connection")
	}
}

func TestDiff_SuspiciousExecutable(t *testing.T) {
	ctx := context.Background()

	execDigest := pbdigest.NewFromBlob([]byte("suspicious binary"))

	old := &inmemory.SysGraph{
		GraphPb:     sgpb.SysGraph_builder{Id: proto.String("old")}.Build(),
		Actions:     make(map[int64]*sgpb.Action),
		ResourceMap: make(map[pbdigest.Digest]*sgpb.Resource),
	}

	new := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{Id: proto.String("new")}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id:                       proto.Int64(1),
				ExecutableResourceDigest: proto.String(execDigest.String()),
			}.Build(),
		},
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			execDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
				FileInfo: sgpb.FileInfo_builder{
					Path:   proto.String("/tmp/downloaded_binary"),
					Digest: proto.String(execDigest.String()),
				}.Build(),
			}.Build(),
		},
	}

	diff, err := Diff(ctx, old, new, DefaultOptions())
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	// Should generate security alert for executable in /tmp.
	found := false
	for _, alert := range diff.SecurityAlerts {
		if alert.Category == "executable" && alert.Severity == "warning" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected warning alert for executable in /tmp")
	}
}

func TestDiff_FileNormalization(t *testing.T) {
	ctx := context.Background()

	oldDigest := pbdigest.NewFromBlob([]byte("old pyc"))
	newDigest := pbdigest.NewFromBlob([]byte("new pyc content"))

	old := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{Id: proto.String("old")}.Build(),
		Actions: make(map[int64]*sgpb.Action),
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			oldDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
				FileInfo: sgpb.FileInfo_builder{
					Path:   proto.String("/app/__pycache__/main.cpython-39.pyc"),
					Digest: proto.String(oldDigest.String()),
				}.Build(),
			}.Build(),
		},
	}

	new := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{Id: proto.String("new")}.Build(),
		Actions: make(map[int64]*sgpb.Action),
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			newDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
				FileInfo: sgpb.FileInfo_builder{
					Path:   proto.String("/app/__pycache__/main.cpython-39.pyc"),
					Digest: proto.String(newDigest.String()),
				}.Build(),
			}.Build(),
		},
	}

	diff, err := Diff(ctx, old, new, DefaultOptions())
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	// The pyc file change should be normalized.
	if len(diff.Files.Changed) != 0 {
		t.Errorf("Expected 0 changed files (should be normalized), got %d", len(diff.Files.Changed))
	}
	if len(diff.Files.Normalized) != 1 {
		t.Errorf("Expected 1 normalized file, got %d", len(diff.Files.Normalized))
	}
}

func TestDiff_String(t *testing.T) {
	diff := &SysGraphDiff{
		OldID: "old-graph",
		NewID: "new-graph",
		SecurityAlerts: []SecurityAlert{
			{Severity: "warning", Category: "executable", Description: "Test alert"},
		},
		Executables: ExecutableDiff{
			Added: []ExecutableChange{
				{Path: "/usr/bin/curl", ActionID: 1, Argv: []string{"curl", "https://example.com"}},
			},
		},
		NormalizedCounts: map[string]int{
			"compiled artifact": 5,
		},
	}

	output := diff.String()

	// Verify key sections are present.
	if !containsString(output, "=== Sysgraph Comparison:") {
		t.Error("Missing header")
	}
	if !containsString(output, "Security Alerts") {
		t.Error("Missing security alerts section")
	}
	if !containsString(output, "Test alert") {
		t.Error("Missing alert content")
	}
	if !containsString(output, "/usr/bin/curl") {
		t.Error("Missing executable")
	}
	if !containsString(output, "Normalized") {
		t.Error("Missing normalized section")
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestNormalizationRules(t *testing.T) {
	tests := []struct {
		name     string
		rule     NormalizationRule
		path     string
		expected bool
	}{
		{"compiled .o", &CompiledArtifactNormalizer{}, "/build/main.o", true},
		{"compiled .pyc", &CompiledArtifactNormalizer{}, "/app/module.pyc", true},
		{"regular .py", &CompiledArtifactNormalizer{}, "/app/module.py", false},
		{"lockfile package-lock", &LockfileNormalizer{}, "/app/package-lock.json", true},
		{"lockfile yarn", &LockfileNormalizer{}, "/app/yarn.lock", true},
		{"regular json", &LockfileNormalizer{}, "/app/config.json", false},
		{"build path tmp", &BuildPathNormalizer{}, "/tmp/build-123/file.txt", true},
		{"build path bazel", &BuildPathNormalizer{}, "bazel-out/k8-fastbuild/file.txt", true},
		{"regular path", &BuildPathNormalizer{}, "/usr/local/file.txt", false},
		{"source map .map", &SourceMapNormalizer{}, "/dist/bundle.js.map", true},
		{"regular js", &SourceMapNormalizer{}, "/dist/bundle.js", false},
		{"pycache", &PycacheNormalizer{}, "/app/__pycache__/main.cpython-39.pyc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
				FileInfo: sgpb.FileInfo_builder{
					Path: proto.String(tt.path),
				}.Build(),
			}.Build()
			got := tt.rule.ShouldNormalize(res, res)
			if got != tt.expected {
				t.Errorf("ShouldNormalize(%s) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}
