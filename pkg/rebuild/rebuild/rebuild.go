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

package rebuild

import (
	"context"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/storage"
)

// Rebuilder defines the operations used to rebuild an ecosystem's packages.
type Rebuilder interface {
	InferRepo(context.Context, Target, RegistryMux) (string, error)
	CloneRepo(context.Context, Target, string, billy.Filesystem, storage.Storer) (RepoConfig, error)
	InferStrategy(context.Context, Target, RegistryMux, *RepoConfig, Strategy) (Strategy, error)
	Rebuild(context.Context, Target, Instructions, billy.Filesystem) error
	Compare(context.Context, Target, Asset, Asset, AssetStore, Instructions) (error, error)
}
