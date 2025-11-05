// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"bytes"
	"context"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

func (r Rebuilder) NeedsPrivilege(input rebuild.Input) bool {
	return false
}

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
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
	rbb := new(bytes.Buffer)
	upb := new(bytes.Buffer)

	{
		rbr, err := assets.Reader(ctx, rb)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to find rebuilt artifact")
		}
		defer rbr.Close()
		if _, err = rbb.ReadFrom(rbr); err != nil {
			return nil, errors.Wrapf(err, "failed to read rebuilt artifact")
		}
	}
	{
		upr, err := assets.Reader(ctx, up)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to find upstream artifact")
		}
		defer upr.Close()
		if _, err = upb.ReadFrom(upr); err != nil {
			return nil, errors.Wrapf(err, "failed to read upstream artifact")
		}
	}

	if rbb.Len() > upb.Len() {
		return errors.New("rebuild is larger than upstream"), nil
	} else if rbb.Len() < upb.Len() {
		return errors.New("upstream is larger than rebuild"), nil
	}
	if !bytes.Equal(upb.Bytes(), rbb.Bytes()) {
		return errors.New("content differences found"), nil
	}
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
