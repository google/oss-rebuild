// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package git

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
