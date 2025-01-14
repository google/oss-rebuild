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

package maven

import (
	"context"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

func (Rebuilder) Rebuild(ctx context.Context, t rebuild.Target, inst rebuild.Instructions, fs billy.Filesystem) error {
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Source); err != nil {
		return errors.Wrap(err, "fetching source")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Deps); err != nil {
		return errors.Wrap(err, "configuring build deps")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Build); err != nil {
		return errors.Wrap(err, "executing build")
	}
	return nil
}

func (Rebuilder) Compare(ctx context.Context, t rebuild.Target, rb rebuild.Asset, up rebuild.Asset, assets rebuild.AssetStore, inst rebuild.Instructions) (msg error, err error) {
	return nil, nil
}

func RebuildMany(ctx context.Context, inputs []rebuild.Input, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no inputs provided")
	}
	for i := range inputs {
		packageVersion, err := mux.Maven.PackageVersion(ctx, inputs[i].Target.Package, inputs[i].Target.Version)
		if err != nil {
			return nil, err
		}
		inputs[i].Target.Artifact = packageVersion.Files[0]
	}
	return rebuild.RebuildMany(ctx, Rebuilder{}, inputs, mux)
}
