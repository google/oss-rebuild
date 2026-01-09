// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/registry/debian/control"
	"github.com/pkg/errors"
)

var (
	registryURL           = urlx.MustParse("https://deb.debian.org/debian/pool/")
	buildinfoURL          = urlx.MustParse("https://buildinfos.debian.net/buildinfo-pool/")
	snapshotURL           = urlx.MustParse("https://snapshot.debian.org/")
	debRegex              = regexp.MustCompile(`^(?P<name>[^_]+)_(?P<version>[^_]+)_(?P<arch>[^_]+)\.deb$`)
	nativeVersionRegex    = regexp.MustCompile(`^((?P<epoch>[0-9]+):)?(?P<upstream_version>[0-9][A-Za-z0-9\.\+\~]*)$`)
	nonNativeVersionRegex = regexp.MustCompile(`^((?P<epoch>[0-9]+):)?(?P<upstream_version>[0-9][A-Za-z0-9\.\+\~\-]*)\-(?P<debian_revision>[A-Za-z0-9\+\.\~]+)$`) // upstream_version is only allowed to contain "-" if debian_revision is non-empty
	// Binary Non-maintainer Upload, end with +b<somenumber>.
	// For native packages this will apply to upstream_version, or the debian_revision for non-native packages.
	// For non-maintainer uploads that include source changes, there are other
	// patterns but we don't care about those because the source version will
	// match the binary version.
	binaryNMURegex = regexp.MustCompile(`^(?P<base>.*)\+b(?P<binaryNMU>[0-9]+)$`)
)

// Debian pakage versions are rather complex. See these docs for details
// https://www.debian.org/doc/debian-policy/ch-controlfields.html#s-f-version

type Version struct {
	// Epoch is used when upstream version number scheme changes.
	Epoch          string
	Upstream       string
	DebianRevision string
}

func ParseVersion(version string) (*Version, error) {
	if matches := nativeVersionRegex.FindStringSubmatch(version); matches != nil {
		return &Version{
			Epoch:    matches[nativeVersionRegex.SubexpIndex("epoch")],
			Upstream: matches[nativeVersionRegex.SubexpIndex("upstream_version")],
		}, nil
	}
	if matches := nonNativeVersionRegex.FindStringSubmatch(version); matches != nil {
		return &Version{
			Epoch:          matches[nonNativeVersionRegex.SubexpIndex("epoch")],
			Upstream:       matches[nonNativeVersionRegex.SubexpIndex("upstream_version")],
			DebianRevision: matches[nonNativeVersionRegex.SubexpIndex("debian_revision")],
		}, nil
	}
	return nil, errors.Errorf("version doesn't match regex: %s", version)
}

func (v *Version) String() string {
	epoch := ""
	if v.Epoch != "" {
		epoch = v.Epoch + ":"
	}
	upstream := v.Upstream
	debianRevision := ""
	if v.DebianRevision != "" {
		debianRevision = "-" + v.DebianRevision
	}
	return epoch + upstream + debianRevision
}

// RollbackBase will return the true upstream, only if there's been a rollback denoted with +really.
// If no rollback version is found, this returns the empty string. Binary versions are stripped if present.
func (v *Version) RollbackBase() string {
	// The presence of +really in the upstream_version component indicates that
	// a newer upstream version has been rolled back to an older upstream version.
	// The part of the upstream_version component following +really is the true upstream version.
	if strings.Contains(v.Upstream, "+really") {
		spl := strings.Split(v.Upstream, "+really")
		return spl[len(spl)-1]
	}
	return ""
}

// Native returns true if a package is "native", meaning the Debian package release used the upstream versioning directly.
func (v *Version) Native() bool {
	return v.DebianRevision == ""
}

// BinaryNonMaintainerUpload returns the version number of the binary-only NMU if this is one.
func (v *Version) BinaryNonMaintainerUpload() string {
	// If it's native and the upstream ends with \+b[0-9]+
	if match := binaryNMURegex.FindStringSubmatch(v.Upstream); v.Native() && match != nil {
		return match[binaryNMURegex.SubexpIndex("binaryNMU")]
	}
	// If it's native and the upstream ends with \+b[0-9]+
	if match := binaryNMURegex.FindStringSubmatch(v.DebianRevision); !v.Native() && match != nil {
		return match[binaryNMURegex.SubexpIndex("binaryNMU")]
	}
	return ""
}

// BinaryIndependentString converts this version into a string, minus any binary-only version identifiers.
func (v *Version) BinaryIndependentString() string {
	return strings.TrimSuffix(v.String(), "+b"+v.BinaryNonMaintainerUpload())
}

type ArtifactIdentifier struct {
	// Name is the name of the artifact (different from the source package)
	Name string
	// Version is the parsed version following https://www.debian.org/doc/debian-policy/ch-controlfields.html#s-f-version
	Version *Version
	// Arch is the target architecture
	Arch string
}

func ParseDebianArtifact(artifact string) (ArtifactIdentifier, error) {
	matches := debRegex.FindStringSubmatch(artifact)
	if matches == nil {
		return ArtifactIdentifier{}, errors.Errorf("unexpected artifact name: %s", artifact)
	}
	name := matches[debRegex.SubexpIndex("name")]
	version, err := ParseVersion(matches[debRegex.SubexpIndex("version")])
	if err != nil {
		return ArtifactIdentifier{}, err
	}
	arch := matches[debRegex.SubexpIndex("arch")]
	return ArtifactIdentifier{
		Name:    name,
		Version: version,
		Arch:    arch,
	}, nil
}

// Registry is a debian package registry.
type Registry interface {
	ArtifactURL(context.Context, string, string) (string, error)
	Artifact(context.Context, string, string, string) (io.ReadCloser, error)
	DSC(context.Context, string, string, string) (string, *control.ControlFile, error)
	BuildInfo(context.Context, string, string, string, string) (string, *control.BuildInfo, error)
}

// HTTPRegistry is a Registry implementation that uses the debian HTTP API.
type HTTPRegistry struct {
	Client httpx.BasicClient
}

func (r HTTPRegistry) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching artifact")
	}
	return resp.Body, nil
}

func poolDir(name string) string {
	// Most packages are in a prefix dir matching their first letter.
	prefixDir := name[0:1]
	// "lib" is such a common prefix that these packages are subdivided into lib* directories.
	if strings.HasPrefix(name, "lib") {
		prefixDir = name[0:4]
	}
	return prefixDir
}

func PoolURL(component, name, artifact string) string {
	u := urlx.Copy(registryURL)
	u.Path += path.Join(component, poolDir(name), name, artifact)
	return u.String()
}

func BuildInfoURL(name, version, arch string) string {
	u := urlx.Copy(buildinfoURL)
	u.Path += path.Join(poolDir(name), name, fmt.Sprintf("%s_%s_%s.buildinfo", name, version, arch))
	return u.String()
}

func (r HTTPRegistry) BuildInfo(ctx context.Context, component, name, version, arch string) (string, *control.BuildInfo, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return "", nil, err
	}
	buildinfoURL := BuildInfoURL(name, v.String(), arch)
	log.Printf("Fetching buildinfo from %s", buildinfoURL)
	re, err := r.get(ctx, buildinfoURL)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to get .buildinfo file %s", buildinfoURL)
	}
	b, err := control.ParseBuildInfo(re)
	return buildinfoURL, b, err
}

func guessDSCURL(component, name string, version *Version) string {
	return PoolURL(component, name, fmt.Sprintf("%s_%s.dsc", name, version.String()))
}

func (r HTTPRegistry) DSC(ctx context.Context, component, name, version string) (string, *control.ControlFile, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return "", nil, err
	}
	DSCURI := guessDSCURL(component, name, v)
	re, err := r.get(ctx, DSCURI)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to get .dsc file %s", DSCURI)
	}
	d, err := control.Parse(re)
	return DSCURI, d, err
}

// fileInfo is metadata about files, such as their name, size, which archive it was seen in and when.
// We are only interested in Name and ArchiveName at the moment.
type fileInfo struct {
	Name        string `json:"name"`
	ArchiveName string `json:"archive_name"`
}

// fileInfoResponse is the response from the binfiles endpoint on the snapshot service.
type fileInfoResponse struct {
	FileInfo map[string][]fileInfo
	// Result maps the architecture to a file hash, allowing you to look up the FileInfo or fetch the file itself.
	Result []struct {
		Architecture string
		Hash         string
	}
}

func (r HTTPRegistry) ArtifactURL(ctx context.Context, name, artifact string) (string, error) {
	// To determine the ArtifactURL, there are a few steps. The following is an example of fetching an artifact from the acl package at version 2.3.2-1+b1
	// First you might need to fetch a list of all versions:
	// https://snapshot.debian.org/mr/binary/acl/
	// However in our case, we already know the version.
	// Next, you need to determine the hash of the correct artifact (architecture and artifact name), which can be found using the /binfiles/ endpoint:
	// https://snapshot.debian.org/mr/package/acl/2.3.2-2/binfiles/libacl1/2.3.2-2+b1?fileinfo=1
	// Finally you have the URL of the artifact directly:
	// https://snapshot.debian.org/file/53f2b0612c8ed8a60970f9a206ae65eb84681f6e
	a, err := ParseDebianArtifact(artifact)
	if err != nil {
		return "", err
	}
	var response fileInfoResponse
	var fileinfoURL *url.URL
	// NOTE: Epochs are used when the source version scheme has changed.
	// Frequently the epoch is dropped from the version identifier used by the package manager
	// (when only one or two versions are available for a given distribution, the source version schemes don't need to be disambiguated).
	// If the artifact doesn't exist under an empty epoch, we try again with epoch "1" which frequently works.
	// If this proves to be insufficient, we can get it from the .buildinfo file (which is stored at a URL missing the epoch but contains a version identifier that includes the epoch).
	// We have not added the buildinfo parsing yet to avoid making an extra call to the build info service if possible.
	for _, epoch := range []string{"", "1"} {
		a.Version.Epoch = epoch
		guessFileinfoURL := urlx.Copy(snapshotURL)
		{
			guessFileinfoURL.Path = path.Join(guessFileinfoURL.Path, "mr/package", name, a.Version.BinaryIndependentString(), "binfiles", a.Name, a.Version.String())
			query := guessFileinfoURL.Query()
			query.Add("fileinfo", "1")
			guessFileinfoURL.RawQuery = query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, guessFileinfoURL.String(), nil)
		if err != nil {
			return "", errors.Wrap(err, "building fileinfo request")
		}
		resp, err := r.Client.Do(req)
		if err != nil {
			return "", errors.Wrap(err, "fetching fileinfo")
		} else if resp.StatusCode == http.StatusNotFound {
			// That fileinfo doesn't exist, try the next epoch.
			log.Printf("Fileinfo url not found: %s", guessFileinfoURL.String())
			continue
		} else if resp.StatusCode != http.StatusOK {
			return "", errors.Wrap(errors.New(resp.Status), "fetching fileinfo")
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return "", err
		}
		// If we succeeded, break out of the loop.
		fileinfoURL = guessFileinfoURL
		break
	}
	if fileinfoURL == nil {
		return "", errors.New("no valid fileinfo")
	}
	var hash string
	for _, f := range response.Result {
		if f.Architecture == a.Arch {
			hash = f.Hash
			break
		}
	}
	if hash == "" {
		return "", errors.New("no matching architecture found")
	}
	// The above process found an artifact of the correct name, version, and archictecture.
	// Next we want to ensure this artifact was found in the main debian repositoriy with the same name.
	// This protects against cases where the snapshot service has an artifact of the correct identifiers but was not seen in the debain repository.
	{
		verified := false
		infos, ok := response.FileInfo[hash]
		if !ok {
			return "", errors.Errorf("no fileinfo at %s, for the hash %s", fileinfoURL, hash)
		}
		for _, info := range infos {
			if info.Name == artifact && info.ArchiveName == "debian" {
				verified = true
				break
			}
		}
		if !verified {
			return "", errors.Errorf("artifact name %s not found in fileinfo:%+v", artifact, response.FileInfo)
		}
	}
	artifactURL := urlx.Copy(snapshotURL)
	artifactURL.Path += path.Join("file", hash)
	return artifactURL.String(), nil
}

// Artifact returns the package artifact for the given package version.
func (r HTTPRegistry) Artifact(ctx context.Context, component, name, artifact string) (io.ReadCloser, error) {
	url, err := r.ArtifactURL(ctx, name, artifact)
	if err != nil {
		return nil, err
	}
	return r.get(ctx, url)
}

var _ Registry = &HTTPRegistry{}
