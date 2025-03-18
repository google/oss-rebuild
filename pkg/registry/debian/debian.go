// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var (
	registryURL           = urlx.MustParse("https://deb.debian.org/debian/pool/")
	buildinfoURL          = urlx.MustParse("https://buildinfos.debian.net/buildinfo-pool/")
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

type ControlStanza struct {
	Fields map[string][]string
}

type DSC struct {
	Stanzas []ControlStanza
}

// Registry is a debian package registry.
type Registry interface {
	Artifact(context.Context, string, string, string) (io.ReadCloser, error)
	DSC(context.Context, string, string, string) (string, *DSC, error)
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
	if resp.StatusCode != 200 {
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

func guessDSCURL(component, name string, version *Version) string {
	return PoolURL(component, name, fmt.Sprintf("%s_%s.dsc", name, version.String()))
}

func parseDSC(r io.ReadCloser) (*DSC, error) {
	b := bufio.NewScanner(r)
	if !b.Scan() {
		return nil, errors.New("failed to scan .dsc file")
	}
	// Skip PGP signature header.
	if strings.HasPrefix(b.Text(), "-----BEGIN PGP SIGNED MESSAGE-----") {
		b.Scan()
	}
	d := DSC{}
	stanza := ControlStanza{Fields: map[string][]string{}}
	var lastField string
	for {
		// Check for PGP signature footer.
		if strings.HasPrefix(b.Text(), "-----BEGIN PGP SIGNATURE-----") {
			break
		}
		line := b.Text()
		if strings.TrimSpace(line) == "" {
			// Handle empty lines as stanza separators.
			if len(stanza.Fields) > 0 {
				d.Stanzas = append(d.Stanzas, stanza)
				stanza = ControlStanza{Fields: map[string][]string{}}
				lastField = ""
			}
		} else if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			// Handle continuation lines.
			if lastField != "" {
				stanza.Fields[lastField] = append(stanza.Fields[lastField], strings.TrimSpace(line))
			} else {
				return nil, errors.Errorf("unexpected continuation line")
			}
		} else {
			// Handle new field.
			field, value, found := strings.Cut(line, ":")
			if !found {
				return nil, errors.Errorf("expected new field: %v", line)
			}
			if _, ok := stanza.Fields[field]; ok {
				return nil, errors.Errorf("duplicate field in stanza: %s", field)
			}
			stanza.Fields[field] = []string{}
			// Skip empty first lines (start of a multiline field).
			if strings.TrimSpace(value) != "" {
				stanza.Fields[field] = []string{strings.TrimSpace(value)}
			}
			lastField = field
		}
		if !b.Scan() {
			break
		}
	}
	// Add the final stanza if it's not empty.
	if len(stanza.Fields) > 0 {
		d.Stanzas = append(d.Stanzas, stanza)
	}

	return &d, nil
}

func (r HTTPRegistry) DSC(ctx context.Context, component, name, version string) (string, *DSC, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return "", nil, err
	}
	DSCURI := guessDSCURL(component, name, v)
	re, err := r.get(ctx, DSCURI)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to get .dsc file %s", DSCURI)
	}
	d, err := parseDSC(re)
	return DSCURI, d, err
}

// Artifact returns the package artifact for the given package version.
func (r HTTPRegistry) Artifact(ctx context.Context, component, name, artifact string) (io.ReadCloser, error) {
	return r.get(ctx, PoolURL(component, name, artifact))
}

var _ Registry = &HTTPRegistry{}
