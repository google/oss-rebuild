// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"io"

	"github.com/go-git/go-billy/v5"
	"google.golang.org/genai"
)

// FileTools returns a list of file-related function definitions for use by the
// LLM. The returned function definitions will read files from the local filesystem
// relative to the provided base directory.
func FileTools(fs billy.Filesystem) []*FunctionDefinition {
	return []*FunctionDefinition{
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "read_file",
				Description: "Fetch the content of the file from the local filesystem",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the file to be read, relative to the base directory"},
					},
					Required: []string{"path"},
				},
				Response: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"content": {Type: genai.TypeString, Description: "The file content, if read was successful"},
						"error":   {Type: genai.TypeString, Description: "The error reading the requested file, if unsuccessful"},
					},
				},
			},
			Function: func(args map[string]any) genai.FunctionResponse {
				var path, content, errStr string
				if patharg, ok := args["path"]; ok {
					if p, ok := patharg.(string); ok && p != "" {
						path = p
					}
				}
				if f, err := fs.Open(path); err != nil {
					errStr = err.Error()
				} else {
					defer f.Close()
					if b, err := io.ReadAll(f); err != nil {
						errStr = err.Error()
					} else {
						content = string(b)
					}
				}

				return genai.FunctionResponse{
					Name: "read_file",
					Response: map[string]any{
						"content": content,
						"error":   errStr,
					},
				}
			},
		},
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "list_dir",
				Description: "Fetch the list of files from the local filesystem directory",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the directory to be read, relative to the base directory. Omit or use empty string for root."},
					},
					Required: []string{},
				},
				Response: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"entries": {Type: genai.TypeArray, Description: "The list of files and directories at the requested path, if read was successful", Items: &genai.Schema{Type: genai.TypeString, Description: "A file path, ending with a slash if a directory"}},
						"error":   {Type: genai.TypeString, Description: "The error listing the requested path, if unsuccessful"},
					},
				},
			},
			Function: func(args map[string]any) genai.FunctionResponse {
				var path string
				if patharg, ok := args["path"]; ok {
					if p, ok := patharg.(string); ok {
						path = p
					}
				}

				var errStr string
				var entries []string
				if dirEntries, err := fs.ReadDir(path); err != nil {
					errStr = err.Error()
				} else {
					entries = make([]string, 0, len(dirEntries))
					for _, e := range dirEntries {
						name := e.Name()
						if e.IsDir() {
							name += "/"
						}
						entries = append(entries, name)
					}
				}

				return genai.FunctionResponse{
					Name: "list_dir",
					Response: map[string]any{
						"entries": entries,
						"error":   errStr,
					},
				}
			},
		},
	}
}
