// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-git/v5/storage"
)

// Storer augments go-git's Storer to provide the capability to re-initialize the underlying state.
type Storer struct {
	storage.Storer
	cbk func() storage.Storer
}

// NewStorer creates and initializes a new Storer.
func NewStorer(init func() storage.Storer) *Storer {
	s := &Storer{cbk: init}
	s.Reset()
	return s
}

// Reset recreates the underlying Storer from the callback.
func (s *Storer) Reset() {
	s.Storer = s.cbk()
}
