// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// GetVersions returns the versions to be processed, most recent to least recent.
func GetVersions(ctx context.Context, pkg string, mux rebuild.RegistryMux) (versions []string, err error) {
	p, err := mux.NPM.Package(ctx, pkg)
	if err != nil {
		return
	}
	for v := range p.Versions {
		// Omit pre-release versions.
		// TODO: Make this configurable.
		if strings.ContainsRune(v, '-') {
			continue
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return p.UploadTimes[versions[i]].After(p.UploadTimes[versions[j]])
	})
	return
}

func sanitize(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "@", ""), "/", "-")
}

func artifactName(t rebuild.Target) string {
	return fmt.Sprintf("%s-%s.tgz", sanitize(t.Package), t.Version)
}

func makeUsrLocalCleanup() func() {
	existing := make(map[string]bool)
	basepath := "/usr/local"
	basefs := osfs.New(basepath)
	util.Walk(basefs, ".", func(path string, info fs.FileInfo, err error) error {
		existing[filepath.Join(basepath, path)] = true
		return nil
	})
	return func() {
		log.Println("cleaning up Node install")
		util.Walk(basefs, ".", func(path string, info fs.FileInfo, err error) error {
			fullpath := filepath.Join(basepath, path)
			if !existing[fullpath] {
				os.RemoveAll(fullpath)
			}
			return nil
		})
	}
}

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

var nodeFetchPat = regexp.MustCompile(`Connecting to unofficial-builds.nodejs.org [^\n]*?\nwget: server returned error: HTTP/1.1 404 Not Found`)

func (Rebuilder) Rebuild(ctx context.Context, t rebuild.Target, inst rebuild.Instructions, fs billy.Filesystem) error {
	defer makeUsrLocalCleanup()()
	if _, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Source); err != nil {
		return errors.Wrap(err, "failed to execute strategy.Source")
	}
	if output, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Deps); err != nil {
		switch {
		case nodeFetchPat.FindString(output) != "":
			return errors.Errorf("node version not found")
		default:
			return errors.Wrap(err, "failed to execute strategy.Deps")
		}
	}
	if output, err := rebuild.ExecuteScript(ctx, fs.Root(), inst.Build); err != nil {
		// Build failed. Let's try to figure out why.
		switch {
		case strings.Contains(output, "primordials is not defined"):
			// TODO: Recovery is to use Node <11.15
			return errors.Errorf("primordials error")
		case strings.Contains(output, "cb.apply is not a function"):
			return errors.Errorf("cb.apply error")
		case strings.Contains(output, ": command not found"):
			endIdx := strings.Index(output, ": command not found")
			startIdx := strings.LastIndex(output[:endIdx], ": ")
			return errors.Errorf("pack command not found: %s", output[startIdx+2:endIdx])
		// TODO: Classify with newly-observed cases.
		default:
			return errors.Wrapf(err, "unknown npm pack failure:\n%s", output)
		}
	}
	return nil
}

var (
	verdictMissingDist        = errors.New("dist/ file(s) found in upstream but not rebuild")
	verdictDSStore            = errors.New(".DS_STORE file(s) found in upstream but not rebuild")
	verdictLineEndings        = errors.New("Excess CRLF line endings found in upstream")
	verdictMismatchedFiles    = errors.New("mismatched file(s) in upstream and rebuild")
	verdictUpstreamOnly       = errors.New("file(s) found in upstream but not rebuild")
	verdictHiddenUpstreamOnly = errors.New("hidden file(s) found in upstream but not rebuild")
	verdictRebuildOnly        = errors.New("file(s) found in rebuild but not upstream")
	verdictPackageJSONDiff    = errors.New("package.json differences found")
	verdictContentDiff        = errors.New("content differences found")
)

func (Rebuilder) Compare(ctx context.Context, t rebuild.Target, rb, up rebuild.Asset, assets rebuild.AssetStore, _ rebuild.Instructions) (msg error, err error) {
	csRB, csUP, err := rebuild.Summarize(ctx, t, rb, up, assets)
	if err != nil {
		return nil, errors.Wrapf(err, "summarizing assets")
	}
	upOnly, diffs, rbOnly := csUP.Diff(csRB)
	var foundDist, foundDSStore bool
	allHidden := true
	for _, f := range upOnly {
		if strings.HasPrefix(f, "package/dist/") {
			foundDist = true
		}
		if strings.HasSuffix(f, "/.DS_STORE") {
			foundDSStore = true
		}
		allHidden = allHidden && strings.HasPrefix(f, "package/.")
	}
	var pkgJSONDiff bool
	for _, f := range diffs {
		if f == "package/package.json" {
			pkgJSONDiff = true
		}
	}
	switch {
	case foundDist:
		return verdictMissingDist, nil
	case foundDSStore:
		return verdictDSStore, nil
	case csUP.CRLFCount > csRB.CRLFCount:
		return verdictLineEndings, nil
	case len(upOnly) > 0 && len(rbOnly) > 0:
		return verdictMismatchedFiles, nil
	case len(upOnly) > 0:
		if allHidden {
			return verdictHiddenUpstreamOnly, nil
		}
		return verdictUpstreamOnly, nil
	case len(rbOnly) > 0:
		return verdictRebuildOnly, nil
	case pkgJSONDiff:
		return verdictPackageJSONDiff, nil
	case len(diffs) > 0:
		return verdictContentDiff, nil
	default:
		return nil, nil
	}
}

// RebuildMany executes rebuilds for each provided rebuild.Input returning their rebuild.Verdicts.
func RebuildMany(ctx context.Context, inputs []rebuild.Input, mux rebuild.RegistryMux) ([]rebuild.Verdict, error) {
	for i := range inputs {
		inputs[i].Target.Artifact = artifactName(inputs[i].Target)
	}
	return rebuild.RebuildMany(ctx, Rebuilder{}, inputs, mux)
}

// RebuildRemote executes the given target strategy on a remote builder.
func RebuildRemote(ctx context.Context, input rebuild.Input, id string, opts rebuild.RemoteOptions) error {
	opts.UseTimewarp = true
	return rebuild.RebuildRemote(ctx, input, id, opts)
}
