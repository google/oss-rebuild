// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/storage"
)

// RepositoryOptions configures the storage and worktree for repositories.
type RepositoryOptions struct {
	Storer   storage.Storer
	Worktree billy.Filesystem
}
