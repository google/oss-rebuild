// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"bytes"
	"context"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

// We expect target.Packge to be in the form "<component>/<name>".
func ParseComponent(pkg string) (component, name string, err error) {
	component, name, found := strings.Cut(pkg, "/")
	if !found {
		return "", "", errors.Errorf("failed to parse debian component: %s", pkg)
	}
	return component, name, nil
}

func (Rebuilder) Rebuild(ctx context.Context, t rebuild.Target, inst rebuild.Instructions, fs billy.Filesystem) error {
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Source); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Source")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Deps); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Deps")
	}
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Build); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Build")
	}
	return nil
}

func (Rebuilder) Compare(ctx context.Context, t rebuild.Target, rb, up rebuild.Asset, assets rebuild.AssetStore, inst rebuild.Instructions) (msg error, err error) {
	// TODO: Add content summary support for deb packages (rebuild.Summarize).
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

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	_, name, err := ParseComponent(t.Package)
	if err != nil {
		return "", errors.Wrap(err, "parsing package name")
	}
	return mux.Debian.ArtifactURL(ctx, name, t.Artifact)
}
