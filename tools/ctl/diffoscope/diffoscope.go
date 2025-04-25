// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffoscope

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

const (
	// DiffAsset captures the diff between rebuild and upstream.
	DiffAsset rebuild.AssetType = "diff"
)

func stabilizeArtifact(in, out string, t rebuild.Target) error {
	orig, err := os.Open(in)
	if err != nil {
		return errors.Wrap(err, "opening input")
	}
	defer orig.Close()
	stabilized, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE, os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "opening output")
	}
	defer stabilized.Close()
	if err := archive.Stabilize(stabilized, orig, t.ArchiveType()); err != nil {
		return errors.Wrap(err, "running stabilize")
	}
	return nil
}

func DiffArtifacts(ctx context.Context, rba, usa string, t rebuild.Target) ([]byte, error) {
	dir, err := os.MkdirTemp("", "*")
	if err != nil {
		return nil, errors.Wrap(err, "creating tempdir")
	}
	defer os.RemoveAll(dir)
	{
		// TODO: We should use the version of Stabilize used in the rebuild.
		stabilized := filepath.Join(dir, "stabilized-"+filepath.Base(rba))
		if err := stabilizeArtifact(rba, stabilized, t); err != nil {
			return nil, errors.Wrap(err, "stabilizing rebuild")
		}
		rba = stabilized
	}
	{
		// TODO: We should use the version of Stabilize used in the rebuild.
		stabilized := filepath.Join(dir, "stabilized-"+filepath.Base(usa))
		if err := stabilizeArtifact(usa, stabilized, t); err != nil {
			return nil, errors.Wrap(err, "stabilizing upstream")
		}
		usa = stabilized
	}
	var script string
	args := fmt.Sprintf(" --no-progress --text-color=always %s %s 2>&1 | less -R", rba, usa)
	if _, err := exec.LookPath("diffoscope"); err == nil {
		script = "diffoscope" + args
	} else if _, err := exec.LookPath("uvx"); err == nil {
		script = "uvx diffoscope" + args
	} else if _, err := exec.LookPath("docker"); err == nil {
		dir := filepath.Dir(usa)
		script = fmt.Sprintf("docker run --rm -t --user $(id -u):$(id -g) -v %s:%s:ro registry.salsa.debian.org/reproducible-builds/diffoscope", dir, dir) + args
	} else {
		log.Println("No execution option found for diffoscope. Attempted {diffoscope,uvx,docker}")
		return nil, errors.New("failed to find diffoscope")
	}
	cmd := exec.Command("bash", "-c", script)
	contents, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrap(err, "running diffoscope")
	}
	return contents, nil
}
