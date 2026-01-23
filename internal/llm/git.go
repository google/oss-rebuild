// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// RepoProvider is a function that returns the current git repository and reference.
type RepoProvider func() (repo *git.Repository, ref string)

// GitTools returns a list of git-related function definitions for use by the
// LLM. The returned function definitions will fetch the current git state from
// the RepoProvider at time of use by the LLM.
func GitTools(rp RepoProvider) []*FunctionDefinition {
	return []*FunctionDefinition{
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "read_repo_file",
				Description: "Fetch the content of the file from the source repository",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the file to be read, relative to the repository root"},
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
				repo, ref := rp()
				path := args["path"].(string)
				var content, errStr string
				if tr, err := getRepoTree(repo, ref); err != nil {
					errStr = err.Error()
				} else {
					content, err = getRepoFile(tr, path)
					if err != nil {
						errStr = err.Error()
					}
				}
				return genai.FunctionResponse{
					Name: "read_repo_file", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"content": content,
						"error":   errStr,
					},
				}
			},
		},
		{
			FunctionDeclaration: genai.FunctionDeclaration{
				Name:        "list_repo_files",
				Description: "Fetch the list of the file from the source repository",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Path of the directory to be read, relative to the repository root. Omit or use empty string for root."}, // Clarified description
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
				repo, ref := rp()
				var path string
				if patharg, ok := args["path"]; ok {
					if p, ok := patharg.(string); ok {
						path = p
					}
					// TODO: Handle case where path exists but is not a string?
				}
				var errStr string
				var content []string
				if tr, err := getRepoTree(repo, ref); err != nil {
					errStr = err.Error()
				} else {
					if content, err = listRepoFiles(tr, path); err != nil {
						errStr = err.Error()
					}
				}
				entries := make([]string, 0, len(content))
				for _, entry := range content {
					entries = append(entries, entry)
				}
				return genai.FunctionResponse{
					Name: "list_repo_files", // Name must match the FunctionDeclaration
					Response: map[string]any{
						"entries": entries,
						"error":   errStr,
					},
				}
			},
		},
	}
}

func getRepoTree(r *git.Repository, commitHash string) (*object.Tree, error) {
	// Get the commit object
	hash := plumbing.NewHash(commitHash)
	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, errors.Wrap(err, "getting commit object")
	}
	// Get the tree for the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, errors.Wrap(err, "getting tree for commit")
	}
	return tree, nil
}

func getRepoFile(tree *object.Tree, path string) (string, error) {
	ent, err := tree.FindEntry(path)
	if err != nil {
		return "", err
	}
	if !ent.Mode.IsFile() {
		return "", errors.New("path does not refer to a file")
	}
	f, err := tree.TreeEntryFile(ent)
	if err != nil {
		return "", err
	}
	return f.Contents()
}

func listRepoFiles(tree *object.Tree, path string) ([]string, error) {
	if path == "" {
		path = "."
	}
	var pathTree *object.Tree
	if path != "." {
		ent, err := tree.FindEntry(path)
		if err != nil {
			return nil, err
		}
		if ent.Mode != filemode.Dir {
			return nil, errors.New("path does not refer to a dir")
		}
		pathTree, err = tree.Tree(path)
		if err != nil {
			return nil, err
		}
	} else {
		pathTree = tree
	}
	var names []string
	for _, ent := range pathTree.Entries {
		if ent.Mode.IsFile() {
			names = append(names, ent.Name)
		} else {
			names = append(names, ent.Name+"/")
		}
	}
	return names, nil
}
