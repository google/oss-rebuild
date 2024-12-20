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
	iofs "io/fs"
	"log"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/pkg/errors"
)

// RebuildOne runs a rebuild for the given package artifact.
// NOTE: err indicates a failed rebuild but the verdict and toUpload returns
// will be valid regardless of its value.
func RebuildOne(ctx context.Context, r Rebuilder, input Input, mux RegistryMux, rcfg *RepoConfig, fs billy.Filesystem, s storage.Storer, assets AssetStore) (verdict Verdict, toUpload []Asset, err error) {
	verdict.Target = input.Target
	t := input.Target
	var repoURI string
	if input.Strategy != nil {
		if hint, ok := input.Strategy.(*LocationHint); ok && hint != nil {
			repoURI = hint.Repo
		} else {
			var inst Instructions
			inst, err = input.Strategy.GenerateFor(t, BuildEnv{})
			if err != nil {
				return
			}
			repoURI = inst.Location.Repo
		}
	} else {
		repoURI, err = r.InferRepo(ctx, t, mux)
		if err != nil {
			return
		}
	}
	repoSetupStart := time.Now()
	if repoURI != rcfg.URI {
		cloneStart := time.Now()
		log.Printf("[%s] Cloning repo '%s' for version '%s'\n", t.Package, repoURI, t.Version)
		if rcfg.URI != "" {
			log.Printf("[%s] Cleaning up previously stored repo '%s'\n", t.Package, rcfg.URI)
			util.RemoveAll(fs, fs.Root())
		}
		var newRepo RepoConfig
		newRepo, err = r.CloneRepo(ctx, t, repoURI, fs, s)
		if err != nil {
			return
		}
		*rcfg = newRepo
		verdict.Timings.CloneEstimate = time.Since(cloneStart)
	} else {
		// Do a fresh checkout to wipe any cruft from previous builds.
		_, err = gitx.Reuse(ctx, s, fs, &git.CloneOptions{URL: rcfg.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
		if err != nil {
			return
		}
	}
	verdict.Timings.Source = time.Since(repoSetupStart)
	inferenceStart := time.Now()
	if lh, ok := input.Strategy.(*LocationHint); ok && lh != nil {
		// If the input was a hint, include it in inference.
		if lh.Ref == "" && lh.Dir != "" {
			// TODO: For each ecosystem, allow ref inference to occur and validate dir.
			err = errors.New("Dir without Ref is not yet supported.")
			return
		}
		log.Printf("[%s] LocationHint provided: %v, running inference...\n", t.Package, *lh)
		verdict.Strategy, err = r.InferStrategy(ctx, t, mux, rcfg, lh)
		if err != nil {
			return
		}
	} else if input.Strategy != nil {
		// If the input was a full strategy, skip inference.
		log.Printf("[%s] Strategy provided, skipping inference.\n", t.Package)
		verdict.Strategy = input.Strategy
	} else {
		// Otherwise, run full inference.
		log.Printf("[%s] No strategy provided, running inference...\n", t.Package)
		verdict.Strategy, err = r.InferStrategy(ctx, t, mux, rcfg, nil)
		if err != nil {
			return
		}
	}
	verdict.Timings.Infer = time.Since(inferenceStart)
	rbenv := BuildEnv{HasRepo: true}
	if tw, ok := ctx.Value(TimewarpID).(string); ok {
		rbenv.TimewarpHost = tw
	}
	inst, err := verdict.Strategy.GenerateFor(t, rbenv)
	if err != nil {
		err = errors.Wrap(err, "failed to generate strategy")
		return
	}
	buildStart := time.Now()
	err = r.Rebuild(ctx, t, inst, fs)
	verdict.Timings.Build = time.Since(buildStart)
	if err != nil {
		return
	}
	rbPath := inst.OutputPath
	_, err = fs.Stat(rbPath)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			err = errors.Wrap(err, "failed to locate artifact")
			return
		}
		err = errors.Wrapf(err, "failed to stat artifact")
		return
	}
	rb, up, err := Stabilize(ctx, t, mux, rbPath, fs, assets)
	if err != nil {
		return
	}
	cmpErr, err := r.Compare(ctx, t, rb, up, assets, inst)
	if err == nil {
		err = cmpErr
	}
	toUpload = append(toUpload, rb, up)
	return
}
