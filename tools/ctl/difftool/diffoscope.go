// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package difftool

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/pkg/errors"
)

// Diffoscope implements Differ using the diffoscope tool.
type Diffoscope struct{}

func (d Diffoscope) AssetType() rebuild.AssetType {
	return DiffoscopeAsset
}

func (d Diffoscope) Diff(ctx context.Context, rebuildPath, upstreamPath string, target rebuild.Target) ([]byte, error) {
	dir, err := os.MkdirTemp("", "*")
	if err != nil {
		return nil, errors.Wrap(err, "creating tempdir")
	}
	defer os.RemoveAll(dir)
	// Get stabilizers for this target
	stabilizers, err := stability.StabilizersForTarget(target)
	if err != nil {
		return nil, errors.Wrap(err, "getting stabilizers")
	}
	// TODO: We should use the version of Stabilize used in the rebuild.
	stabilizedRebuild := filepath.Join(dir, "stabilized-"+filepath.Base(rebuildPath))
	if err := stabilizeToFile(rebuildPath, stabilizedRebuild, target, stabilizers); err != nil {
		return nil, errors.Wrap(err, "stabilizing rebuild")
	}
	// TODO: We should use the version of Stabilize used in the rebuild.
	stabilizedUpstream := filepath.Join(dir, "stabilized-"+filepath.Base(upstreamPath))
	if err := stabilizeToFile(upstreamPath, stabilizedUpstream, target, stabilizers); err != nil {
		return nil, errors.Wrap(err, "stabilizing upstream")
	}
	var script string
	args := fmt.Sprintf(" --no-progress --text-color=always %s %s 2>&1 | less -R", stabilizedRebuild, stabilizedUpstream)
	if _, err := exec.LookPath("diffoscope"); err == nil {
		script = "diffoscope" + args
	} else if _, err := exec.LookPath("uvx"); err == nil {
		script = "uvx diffoscope" + args
	} else if _, err := exec.LookPath("docker"); err == nil {
		dir := filepath.Dir(stabilizedUpstream)
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
