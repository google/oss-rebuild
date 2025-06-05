// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

func (r Rebuilder) RebuildRemote(ctx context.Context, input rebuild.Input, opts rebuild.RemoteOptions) error {
	opts.UseTimewarp = false
	return rebuild.RebuildRemote(ctx, input, opts)
}

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

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// Assuming the primary artifact is a .jar file.
	return mux.Maven.ReleaseURL(ctx, t.Package, t.Version, ".jar")
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
