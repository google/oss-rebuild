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

package debian

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
)

var (
	registryURL         = "https://deb.debian.org/debian"
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
		return nil, errors.Errorf("fetching artifact: %v", resp.Status)
	}
	return resp.Body, nil
}

func PoolURL(component, name, artifact string) string {
	// Most packages are in a prefix dir matching their first letter.
	prefixDir := name[0:1]
	// "lib" is such a common prefix that these packages are subdivided into lib* directories.
	if strings.HasPrefix(name, "lib") {
		prefixDir = name[0:4]
	}
	return registryURL + fmt.Sprintf("/pool/%s/%s/%s/%s", component, prefixDir, name, artifact)
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
		return "", nil, errors.Wrap(err, "failed to wget .dsc file")
	}
	d, err := parseDSC(re)
	return DSCURI, d, err
}

func ArtifactName(name, version string) string {
	// TODO: Add support for other platforms.
	return fmt.Sprintf("%s_%s_amd64.deb", name, version)
}

// Artifact returns the package artifact for the given package version.
func (r HTTPRegistry) Artifact(ctx context.Context, component, name, artifact string) (io.ReadCloser, error) {
	return r.get(ctx, PoolURL(component, name, artifact))
}

var _ Registry = &HTTPRegistry{}
