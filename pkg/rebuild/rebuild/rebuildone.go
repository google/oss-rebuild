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

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/pkg/errors"
)

// RebuildOne runs a rebuild for the given package artifact.
func RebuildOne(ctx context.Context, r Rebuilder, input Input, mux RegistryMux, rcfg *RepoConfig, fs billy.Filesystem, s storage.Storer, assets AssetStore) (*Verdict, []Asset, error) {
	t := input.Target
	var repoURI string
	if input.Strategy != nil {
		if hint, ok := input.Strategy.(*LocationHint); ok && hint != nil {
			repoURI = hint.Repo
		} else {
			inst, err := input.Strategy.GenerateFor(t, BuildEnv{})
			if err != nil {
				return nil, nil, err
			}
			repoURI = inst.Location.Repo
		}
	} else {
		var err error
		repoURI, err = r.InferRepo(ctx, t, mux)
		if err != nil {
			return nil, nil, err
		}
	}
	repoSetupStart := time.Now()
	var cloneTime time.Duration
	if repoURI != rcfg.URI {
		cloneStart := time.Now()
		log.Printf("[%s] Cloning repo '%s' for version '%s'\n", t.Package, repoURI, t.Version)
		if rcfg.URI != "" {
			log.Printf("[%s] Cleaning up previously stored repo '%s'\n", t.Package, rcfg.URI)
			util.RemoveAll(fs, fs.Root())
		}
		newRepo, err := r.CloneRepo(ctx, t, repoURI, fs, s)
		if err != nil {
			return nil, nil, err
		}
		*rcfg = newRepo
		cloneTime = time.Since(cloneStart)
	} else {
		// Do a fresh checkout to wipe any cruft from previous builds.
		_, err := gitx.Reuse(ctx, s, fs, &git.CloneOptions{URL: rcfg.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
		if err != nil {
			return nil, nil, err
		}
	}
	repoSetupTime := time.Since(repoSetupStart)
	inferenceStart := time.Now()
	var strategy Strategy
	if lh, ok := input.Strategy.(*LocationHint); ok && lh != nil {
		// If the input was a hint, include it in inference.
		if lh.Ref == "" && lh.Dir != "" {
			// TODO: For each ecosystem, allow ref inference to occur and validate dir.
			return nil, nil, errors.New("Dir without Ref is not yet supported.")
		}
		var err error
		log.Printf("[%s] LocationHint provided: %v, running inference...\n", t.Package, *lh)
		strategy, err = r.InferStrategy(ctx, t, mux, rcfg, lh)
		if err != nil {
			return nil, nil, err
		}
	} else if input.Strategy != nil {
		// If the input was a full strategy, skip inference.
		log.Printf("[%s] Strategy provided, skipping inference.\n", t.Package)
		strategy = input.Strategy
	} else {
		// Otherwise, run full inference.
		var err error
		log.Printf("[%s] No strategy provided, running inference...\n", t.Package)
		strategy, err = r.InferStrategy(ctx, t, mux, rcfg, nil)
		if err != nil {
			return nil, nil, err
		}
	}
	inferenceTime := time.Since(inferenceStart)
	rbenv := BuildEnv{HasRepo: true}
	if tw, ok := ctx.Value(TimewarpID).(string); ok {
		rbenv.TimewarpHost = tw
	}
	inst, err := strategy.GenerateFor(t, rbenv)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to generate strategy")
	}
	buildStart := time.Now()
	err = r.Rebuild(ctx, t, inst, fs)
	buildTime := time.Since(buildStart)
	if err != nil {
		return nil, nil, err
	}
	rbPath := inst.OutputPath
	_, err = fs.Stat(rbPath)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return &Verdict{Target: t, Message: errors.Wrap(err, "failed to locate artifact").Error(), Strategy: strategy}, []Asset{}, nil
		}
		return nil, nil, errors.Wrapf(err, "failed to stat artifact")
	}
	rb, up, err := Stabilize(ctx, t, mux, rbPath, fs, assets)
	if err != nil {
		return nil, nil, err
	}
	cmpErr, err := r.Compare(ctx, t, rb, up, assets, inst)
	var msg string
	if err != nil {
		msg = err.Error()
	} else if cmpErr != nil {
		msg = cmpErr.Error()
	}
	return &Verdict{
		Target:   t,
		Message:  msg,
		Strategy: strategy,
		Timings: Timings{
			CloneEstimate: cloneTime,
			Source:        repoSetupTime,
			Infer:         inferenceTime,
			Build:         buildTime,
		},
	}, []Asset{rb, up}, nil
}
