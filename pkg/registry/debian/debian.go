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
	registryURL         = urlx.MustParse("https://deb.debian.org/debian/pool/")
	buildinfoURL        = urlx.MustParse("https://buildinfos.debian.net/buildinfo-pool/")
	binaryReleaseRegexp = regexp.MustCompile(`(\+b[\d\.]+)$`)
)

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

func guessDSCURL(component, name, version string) string {
	cleanVersion := binaryReleaseRegexp.ReplaceAllString(version, "")
	return PoolURL(component, name, fmt.Sprintf("%s_%s.dsc", name, cleanVersion))
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
	DSCURI := guessDSCURL(component, name, version)
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
