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
	"fmt"
	"log"
	"runtime/debug"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	gitinternal "github.com/google/oss-rebuild/internal/git"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	mvnreg "github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/pkg/errors"
)

func rebuildOne(ctx context.Context, input rebuild.Input, rcfg *RepoConfig, fs billy.Filesystem, s storage.Storer) (verdict error, err error) {
	t := input.Target
	pom, err := mvnreg.VersionPomXML(t.Package, t.Version)
	if err != nil {
		return
	}
	uri, err := uri.CanonicalizeRepoURI(pom.Repo())
	if err != nil {
		return
	}
	if uri != rcfg.URI {
		log.Printf("[%s] Cloning repo '%s' for version '%s'\n", t.Package, uri, t.Version)
		if rcfg.URI != "" {
			log.Printf("[%s] Cleaning up previously stored repo '%s'\n", t.Package, rcfg.URI)
			util.RemoveAll(fs, fs.Root())
		}
		var newRepo RepoConfig
		newRepo, err = doRepoInferenceAndClone(ctx, t.Package, &pom, fs, s)
		if err != nil {
			return
		}
		*rcfg = newRepo
	} else {
		// Do a fresh checkout to wipe any cruft from previous builds.
		_, err := gitinternal.Reuse(ctx, s, fs, &git.CloneOptions{URL: rcfg.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
		if err != nil {
			return nil, err
		}
	}
	cfg, err := doInference(ctx, t, rcfg)
	if err != nil {
		return
	}
	cfg.Repo = rcfg.URI
	err = errors.New("Found")
	return
}

// RebuildMany executes rebuilds for each provided rebuild.Input returning their rebuild.Verdicts.
func RebuildMany(ctx context.Context, name string, inputs []rebuild.Input) ([]rebuild.Verdict, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no inputs provided")
	}
	srcPath := fmt.Sprintf("%s/%s", string(rebuild.Maven), strings.ReplaceAll(name, "/", "!"))
	fs, err := osfs.New(".").Chroot(srcPath)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to chroot to source")
	}
	gitfs, err := fs.Chroot(".git")
	if err != nil {
		return nil, errors.Wrapf(err, "Failure chroot to git")
	}
	s := gitinternal.NewStorer(func() storage.Storer {
		return filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: false})
	})
	var rcfg RepoConfig
	var failures []error
	// TODO: Setup logging for each version.
	// Protect against panics in rebuildOne.
	safeRebuildOne := func(input rebuild.Input) {
		defer func() {
			if panicval := recover(); panicval != nil {
				log.Printf("Rebuild panic: %v\n", panicval)
				log.Println(string(debug.Stack()))
				failures = append(failures, errors.Errorf("rebuild panic: %v", panicval))
			}
		}()
		failure, err := rebuildOne(ctx, input, &rcfg, fs, s)
		if err != nil {
			failures = append(failures, errors.Wrapf(err, "rebuild failure"))
		} else {
			failures = append(failures, failure)
		}
	}
	verdicts := make([]rebuild.Verdict, 0, len(failures))
	for i := range inputs {
		// TODO find the correct artifact name.
		inputs[i].Target.Artifact = "default-artifact"
		verdicts = append(verdicts, rebuild.Verdict{
			Target:  inputs[i].Target,
			Message: failures[i].Error(),
		})
	}
	for _, input := range inputs {
		log.Printf("Running a maven rebuildOne: %s %s", input.Target.Package, input.Target.Version)
		safeRebuildOne(input)
	}
	return verdicts, nil
}
