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
	"bytes"
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"time"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	cacheinternal "github.com/google/oss-rebuild/internal/cache"
	gitinternal "github.com/google/oss-rebuild/internal/git"
	"github.com/pkg/errors"
)

// RepoConfig describes the repo currently being used.
type RepoConfig struct {
	*git.Repository
	URI    string
	Dir    string
	RefMap map[string]string
}

// RebuildMany executes rebuilds for each provided rebuild.Input returning their rebuild.Verdicts.
func RebuildMany(ctx context.Context, rebuilder Rebuilder, inputs []Input, registry RegistryMux) ([]Verdict, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no inputs provided")
	}
	c := cacheinternal.NewHierarchicalCache(&cacheinternal.CoalescingMemoryCache{})
	registry, err := RegistryMuxWithCache(registry, c)
	if err != nil {
		return nil, errors.Wrap(err, "creating cached registry")
	}
	go warmCacheForPackage(ctx, registry, inputs[0].Target)
	var fs billy.Filesystem // fs will be {oss-rebuild root}/{ecosystem}/sanitize({package})/
	{
		// Setup the workdir `fs`.
		packageName := inputs[0].Target.Package
		ecosystem := inputs[0].Target.Ecosystem
		for _, input := range inputs {
			if input.Target.Ecosystem != ecosystem {
				return nil, errors.Errorf("inputs should all be for the same ecosystem: %s != %s", input.Target.Ecosystem, ecosystem)
			}
			if input.Target.Package != packageName {
				return nil, errors.Errorf("inputs should all be for the same package: %s != %s", input.Target.Package, packageName)
			}
		}
		srcPath := fmt.Sprintf("%s/%s", string(ecosystem), strings.ReplaceAll(packageName, "/", "!"))
		var err error
		// TODO: Instead of osfs.New(".") pass `fs` into RebuildMany and use that.
		fs, err = osfs.New(".").Chroot(srcPath)
		if err != nil {
			return nil, errors.Wrap(err, "failed to chroot to srcPath")
		}
	}
	// TODO: Use the fs passed into Rebuild many rather than osfs.New(".")
	assetDir, ok := ctx.Value(AssetDirID).(string)
	if !ok {
		return nil, errors.New("no asset dir provided")
	}
	assetsFS, err := osfs.New(".").Chroot(assetDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to chroot to assets")
	}
	localAssets := NewFilesystemAssetStore(assetsFS)
	debugStorer, err := DebugStoreFromContext(ctx)
	if err == ErrNoUploadPath {
		debugStorer = nil
	} else if err != nil {
		return nil, err
	}
	var rcfg RepoConfig
	// TODO: Move the inference portion of this logic to Infer
	gitfs, err := fs.Chroot(".git")
	if err != nil {
		return nil, errors.Wrap(err, "failed to chroot to .git")
	}
	s := gitinternal.NewStorer(func() storage.Storer {
		return filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: false})
	})
	var verdicts []Verdict
	safeRebuildOne := func(input Input) {
		t := input.Target
		defer func() {
			if panicval := recover(); panicval != nil {
				log.Printf("Rebuild panic: %v\n", panicval)
				log.Println(string(debug.Stack()))
				verdicts = append(verdicts, Verdict{Target: t, Message: fmt.Sprintf("rebuild panic: %v", panicval)})
			}
		}()
		// TODO: Duplicate repo inference logs to each associated version.
		// Setup scoped logging.
		logbuf := new(bytes.Buffer)
		resetLogger := ScopedLogCapture(log.Default(), logbuf)
		verdict, assets, err := RebuildOne(ctx, rebuilder, input, registry, &rcfg, fs, s, localAssets)
		if err != nil {
			verdicts = append(verdicts, Verdict{Target: t, Message: err.Error()})
		} else {
			verdicts = append(verdicts, *verdict)
		}
		resetLogger()
		{
			asset := Asset{Type: DebugLogsAsset, Target: t}
			w, _, err := localAssets.Writer(ctx, asset)
			if err != nil {
				log.Printf("Failed to create writer for log asset: %v\n", err)
			} else {
				defer w.Close()
				if _, err := w.Write(logbuf.Bytes()); err != nil {
					log.Printf("Failed to store logs: %v\n", err)
				} else {
					assets = append(assets, asset)
				}
			}
			// Empty logbuf because we're about to do more in-memory file stuff.
			logbuf.Reset()
		}
		if debugStorer != nil {
			for _, asset := range assets {
				if _, err := AssetCopy(ctx, debugStorer, localAssets, asset); err != nil {
					log.Printf("Failed to upload asset to debug storer: %v\n", err)
				}
			}
		}
	}
	for _, input := range inputs {
		log.Printf("Rebuilding %s %s", input.Target.Package, input.Target.Version)
		c.Push(&cacheinternal.CoalescingMemoryCache{})
		go warmCacheforArtifact(ctx, registry, input.Target)
		safeRebuildOne(input)
		c.Pop()
	}
	if retain, ok := ctx.Value(RetainArtifactsID).(bool); ok && !retain {
		util.RemoveAll(fs, fs.Root())
		util.RemoveAll(assetsFS, assetsFS.Root())
	}
	// For builds that re-used the previous repo, track the previous clone time.
	for i := range verdicts {
		if i == 0 {
			continue
		}
		if verdicts[i].Timings.CloneEstimate == time.Duration(0) {
			verdicts[i].Timings.CloneEstimate = verdicts[i-1].Timings.CloneEstimate
		}
	}
	return verdicts, nil
}
