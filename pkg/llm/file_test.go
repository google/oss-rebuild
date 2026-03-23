// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"slices"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
)

func getTestFS(t *testing.T) billy.Filesystem {
	t.Helper()
	fs := memfs.New()
	if err := util.WriteFile(fs, "a.txt", []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll("subdir", 0755); err != nil {
		t.Fatal(err)
	}
	if err := util.WriteFile(fs, "subdir/b.txt", []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	return fs
}

func TestFileTools_ReadFile(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "existing file",
			path: "a.txt",
			want: "hello",
		},
		{
			name: "subdir file",
			path: "subdir/b.txt",
			want: "world",
		},
		{
			name:    "non-existent file",
			path:    "non-existent.txt",
			wantErr: true,
		},
		{
			name:    "read directory",
			path:    "subdir",
			wantErr: true,
		},
	}

	tools := FileTools(getTestFS(t))
	readTool := getTool(tools, "read_file")
	if readTool == nil {
		t.Fatal("read_file tool not found")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := readTool.Function(map[string]any{"path": tt.path})

			if tt.wantErr {
				if resp.Response["error"] == "" {
					t.Fatalf("expected error, got none")
				}
				return
			}

			if resp.Response["error"] != "" {
				t.Fatalf("unexpected error: %v", resp.Response["error"])
			}

			content, ok := resp.Response["content"].(string)
			if !ok {
				t.Fatalf("invalid type for content: %T", resp.Response["content"])
			}

			if content != tt.want {
				t.Errorf("expected %q, got %q", tt.want, content)
			}
		})
	}
}

func TestFileTools_ListDir(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    []string
		wantErr bool
	}{
		{
			name: "root dir",
			path: "",
			want: []string{"a.txt", "subdir/"},
		},
		{
			name: "root dir alias",
			path: ".",
			want: []string{"a.txt", "subdir/"},
		},
		{
			name: "sub dir",
			path: "subdir",
			want: []string{"b.txt"},
		},
		{
			name: "sub dir trailing slash",
			path: "subdir/",
			want: []string{"b.txt"},
		},
		{
			name:    "non-existent dir",
			path:    "non-existent",
			want:    nil,
			wantErr: true,
		},
	}

	tools := FileTools(getTestFS(t))
	listTool := getTool(tools, "list_dir")
	if listTool == nil {
		t.Fatal("list_dir tool not found")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := listTool.Function(map[string]any{"path": tt.path})

			if tt.wantErr {
				if resp.Response["error"] == "" {
					t.Fatalf("expected error, got none")
				}
				return
			}

			if resp.Response["error"] != "" {
				t.Fatalf("unexpected error: %v", resp.Response["error"])
			}

			entries, ok := resp.Response["entries"].([]string)
			if !ok {
				t.Fatalf("invalid type for entries: %T", resp.Response["entries"])
			}

			for _, w := range tt.want {
				if !slices.Contains(entries, w) {
					t.Errorf("entries missing %s", w)
				}
			}
		})
	}
}

func getTool(tools []*FunctionDefinition, name string) *FunctionDefinition {
	for _, tool := range tools {
		if tool.FunctionDeclaration.Name == name {
			return tool
		}
	}
	return nil
}
