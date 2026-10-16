// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// This program generates cibuildwheel_images.go from the x86_64 build images
// that each cibuildwheel release pins in pinned_docker_images.cfg.
//
// Run: go generate ./pkg/rebuild/pypi/platform
//
// The program:
//  1. Lists the final cibuildwheel releases and their upload times on PyPI
//  2. Reads the x86_64 pins at the tag of each release on GitHub
//  3. Keeps the pins of the image repositories that SelectBaseImage returns
//  4. Resolves pinned tags to manifest digests using the quay.io API
//  5. Writes the pins of each release, sorted by version
//
// Older releases pin tags. Newer releases pin digests and note the tag in a
// trailing comment.
//
// A tag that has expired on quay.io keeps the digest that the current table
// records for it, so that regenerating never drops a pin. The pinned image
// may no longer be pullable, but keeping it means that the strategy inferred
// for an upload does not depend on when the table was last generated, and
// that such a build fails on the pull rather than running in another image.
// Tags that expired before their digest was ever recorded are written
// without a digest, which the lookups treat as unknown.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pkg/errors"
)

const (
	releasesURL = "https://pypi.org/pypi/cibuildwheel/json"
	// NOTE: Releases that predate the pin file return 404.
	pinsURL     = "https://raw.githubusercontent.com/pypa/cibuildwheel/v%s/cibuildwheel/resources/pinned_docker_images.cfg"
	tagURL      = "https://quay.io/api/v1/repository/%s/tag/?specificTag=%s&onlyActiveTags=true"
	outputFile  = "cibuildwheel_images.go"
	maxAttempts = 5
)

// supportedRepos maps the image repositories that SelectBaseImage returns to
// the names of their constants.
var supportedRepos = map[string]string{
	"quay.io/pypa/manylinux2014_x86_64":  "ImageManylinux2014X86_64",
	"quay.io/pypa/manylinux_2_28_x86_64": "ImageManylinux2_28X86_64",
	"quay.io/pypa/manylinux_2_34_x86_64": "ImageManylinux2_34X86_64",
	"quay.io/pypa/musllinux_1_1_x86_64":  "ImageMusllinux1_1X86_64",
	"quay.io/pypa/musllinux_1_2_x86_64":  "ImageMusllinux1_2X86_64",
}

var (
	// finalVersion matches the versions that internal/semver orders and
	// excludes pre-releases such as 3.0.0b1.
	finalVersion  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// tagPattern is the tag grammar of the OCI distribution spec.
	tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

var (
	client      = &http.Client{Timeout: time.Minute}
	errNotFound = errors.New("not found")
)

type release struct {
	version   string
	published time.Time
	// pins maps supported repositories to their pins.
	pins map[string]pin
}

type pin struct {
	tag, digest string
}

func main() {
	log.SetFlags(0)
	recorded, err := recordedDigests()
	if err != nil {
		log.Fatal(errors.Wrap(err, "reading recorded digests"))
	}
	releases, err := fetchReleases()
	if err != nil {
		log.Fatal(errors.Wrap(err, "listing releases"))
	}
	log.Printf("Found %d final releases on PyPI", len(releases))
	var pinning []release
	for _, rel := range releases {
		pins, err := fetchPins(rel.version)
		switch {
		case errors.Is(err, errNotFound):
			log.Printf("%s: no pin file", rel.version)
		case err != nil:
			log.Fatal(errors.Wrapf(err, "fetching pins of %s", rel.version))
		case len(pins) == 0:
			log.Printf("%s: no supported pins", rel.version)
		default:
			rel.pins = pins
			pinning = append(pinning, rel)
		}
	}
	if err := resolveDigests(pinning, recorded); err != nil {
		log.Fatal(errors.Wrap(err, "resolving digests"))
	}
	if err := writeTable(pinning); err != nil {
		log.Fatal(errors.Wrap(err, "writing table"))
	}
	log.Printf("Wrote %d releases to %s", len(pinning), outputFile)
}

// fetchReleases returns the final cibuildwheel releases with the time of their
// first upload to PyPI, sorted by version.
func fetchReleases() ([]release, error) {
	body, err := get(releasesURL)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Releases map[string][]struct {
			UploadTime time.Time `json:"upload_time_iso_8601"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.Wrap(err, "parsing releases")
	}
	var releases []release
	for version, files := range resp.Releases {
		if !finalVersion.MatchString(version) || len(files) == 0 {
			continue
		}
		published := files[0].UploadTime
		for _, f := range files[1:] {
			if f.UploadTime.Before(published) {
				published = f.UploadTime
			}
		}
		releases = append(releases, release{version: version, published: published.UTC().Truncate(time.Second)})
	}
	slices.SortFunc(releases, func(a, b release) int { return semver.Cmp(a.version, b.version) })
	return releases, nil
}

// fetchPins returns the pins of supported repositories in the x86_64 section
// of the pin file at the tag of version.
func fetchPins(version string) (map[string]pin, error) {
	body, err := get(fmt.Sprintf(pinsURL, version))
	if err != nil {
		return nil, err
	}
	pins := make(map[string]pin)
	for _, value := range sectionValues(string(body), "x86_64") {
		repo, p := parsePin(value)
		if _, ok := supportedRepos[repo]; !ok {
			continue
		}
		if err := p.validate(); err != nil {
			return nil, errors.Wrapf(err, "parsing pin %q", value)
		}
		if prev, ok := pins[repo]; ok && prev != p {
			return nil, errors.Errorf("conflicting pins for %s", repo)
		}
		pins[repo] = p
	}
	return pins, nil
}

// sectionValues returns the values of the entries in an INI section.
func sectionValues(cfg, section string) []string {
	var values []string
	var current string
	for line := range strings.Lines(cfg) {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "["); ok {
			current = strings.TrimSuffix(name, "]")
			continue
		}
		if _, value, ok := strings.Cut(line, "="); ok && current == section && !strings.HasPrefix(line, "#") {
			values = append(values, strings.TrimSpace(value))
		}
	}
	return values
}

// parsePin parses a pin like "quay.io/pypa/manylinux2014_x86_64:2021-08-03-e7edb37"
// or "quay.io/pypa/manylinux2014_x86_64@sha256:<digest>  # 2026.10.03-1" into
// the repository and the pin.
func parsePin(value string) (string, pin) {
	ref, comment, _ := strings.Cut(value, "#")
	repo, digest, _ := strings.Cut(strings.TrimSpace(ref), "@")
	p := pin{digest: digest}
	// NOTE: Only a colon after the last slash separates a tag. Others separate
	// a registry port.
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo, p.tag = repo[:i], repo[i+1:]
	}
	if comment = strings.TrimSpace(comment); p.tag == "" && tagPattern.MatchString(comment) {
		p.tag = comment
	}
	return repo, p
}

func (p pin) validate() error {
	switch {
	case p.tag == "" && p.digest == "":
		return errors.New("missing tag and digest")
	case p.tag != "" && !tagPattern.MatchString(p.tag):
		return errors.Errorf("invalid tag %q", p.tag)
	case p.digest != "" && !digestPattern.MatchString(p.digest):
		return errors.Errorf("invalid digest %q", p.digest)
	}
	return nil
}

// resolveDigests looks up the digests of pins that only have a tag. A tag that
// no longer exists keeps the digest recorded for it, if any. Otherwise it
// leaves the digest empty.
func resolveDigests(releases []release, recorded map[string]string) error {
	// digests caches lookups by tagged reference because consecutive releases
	// often pin the same tags.
	digests := make(map[string]string)
	for _, rel := range releases {
		for repo, p := range rel.pins {
			if p.digest != "" {
				continue
			}
			ref := repo + ":" + p.tag
			digest, ok := digests[ref]
			if !ok {
				var err error
				if digest, err = resolveDigest(repo, p.tag); err != nil {
					return errors.Wrapf(err, "resolving %s", ref)
				}
				switch prev, wasRecorded := recorded[ref]; {
				case digest == "" && wasRecorded:
					log.Printf("%s: tag %s expired, keeping its recorded digest", rel.version, ref)
					digest = prev
				case digest == "":
					log.Printf("%s: tag %s expired", rel.version, ref)
				case wasRecorded && prev != digest:
					log.Printf("%s: tag %s moved from %s to %s", rel.version, ref, prev, digest)
				}
				digests[ref] = digest
			}
			p.digest = digest
			rel.pins[repo] = p
		}
	}
	return nil
}

// recordedDigests returns the digests that the current table records, keyed
// by tagged reference. It returns nothing if there is no table yet.
func recordedDigests() (map[string]string, error) {
	constants := make(map[string]string, len(supportedRepos))
	for repo, name := range supportedRepos {
		constants[name] = repo
	}
	f, err := parser.ParseFile(token.NewFileSet(), outputFile, nil, parser.SkipObjectResolution)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "parsing %s", outputFile)
	}
	digests := make(map[string]string)
	// NOTE: The table is written by writeTable, so every pin is a key-value
	// pair of an image constant and a literal with string fields.
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		repo, ok := constants[key.Name]
		if !ok {
			return true
		}
		if tag, digest := pinFields(kv.Value); tag != "" && digest != "" {
			digests[repo+":"+tag] = digest
		}
		return false
	})
	return digests, nil
}

// pinFields returns the Tag and Digest fields of a pin literal.
func pinFields(expr ast.Expr) (tag, digest string) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return "", ""
	}
	for _, elt := range lit.Elts {
		field, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok {
			continue
		}
		value, ok := field.Value.(*ast.BasicLit)
		if !ok {
			continue
		}
		unquoted, err := strconv.Unquote(value.Value)
		if err != nil {
			continue
		}
		switch name.Name {
		case "Tag":
			tag = unquoted
		case "Digest":
			digest = unquoted
		}
	}
	return tag, digest
}

// resolveDigest returns the manifest digest of an active tag in a quay.io
// repository, or an empty string if the tag does not exist.
func resolveDigest(repo, tag string) (string, error) {
	path, ok := strings.CutPrefix(repo, "quay.io/")
	if !ok {
		return "", errors.Errorf("unsupported registry in %s", repo)
	}
	body, err := get(fmt.Sprintf(tagURL, path, url.QueryEscape(tag)))
	if err != nil {
		return "", err
	}
	var resp struct {
		Tags []struct {
			Name           string `json:"name"`
			ManifestDigest string `json:"manifest_digest"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", errors.Wrap(err, "parsing tags")
	}
	for _, t := range resp.Tags {
		if t.Name != tag {
			continue
		}
		if !digestPattern.MatchString(t.ManifestDigest) {
			return "", errors.Errorf("invalid digest %q", t.ManifestDigest)
		}
		return t.ManifestDigest, nil
	}
	return "", nil
}

// get returns the body at u and retries transient failures. It returns
// errNotFound if the resource does not exist.
func get(u string) ([]byte, error) {
	var lastErr error
	for attempt := range maxAttempts {
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
		resp, err := client.Get(u)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, errNotFound
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = errors.Errorf("unexpected status %s", resp.Status)
		default:
			return nil, errors.Errorf("fetching %s: unexpected status %s", u, resp.Status)
		}
	}
	return nil, errors.Wrapf(lastErr, "fetching %s", u)
}

const header = `// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Code generated by gen_cibuildwheel_images.go. DO NOT EDIT.
// Regenerate: go generate ./pkg/rebuild/pypi/platform
// Generated at: %s

package platform

import "time"

// CibuildwheelPins lists the cibuildwheel releases that pin a supported x86_64
// build image, sorted by version.
var CibuildwheelPins = CibuildwheelPinTable{
`

// writeTable writes the generated source file atomically.
func writeTable(releases []release) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, header, time.Now().UTC().Format(time.RFC3339))
	for _, rel := range releases {
		t := rel.published
		fmt.Fprintf(&buf, "{\nVersion: %q,\n", rel.version)
		fmt.Fprintf(&buf, "Published: time.Date(%d, time.%s, %d, %d, %d, %d, 0, time.UTC),\n", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
		buf.WriteString("Images: map[string]PinnedImage{\n")
		for _, repo := range slices.Sorted(maps.Keys(rel.pins)) {
			p := rel.pins[repo]
			var fields []string
			if p.tag != "" {
				fields = append(fields, fmt.Sprintf("Tag: %q", p.tag))
			}
			if p.digest != "" {
				fields = append(fields, fmt.Sprintf("Digest: %q", p.digest))
			}
			fmt.Fprintf(&buf, "%s: {%s},\n", supportedRepos[repo], strings.Join(fields, ", "))
		}
		buf.WriteString("},\n},\n")
	}
	buf.WriteString("}\n")
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return errors.Wrap(err, "formatting generated code")
	}
	tmp := outputFile + ".tmp"
	if err := os.WriteFile(tmp, formatted, 0644); err != nil {
		return errors.Wrap(err, "writing temp file")
	}
	if err := os.Rename(tmp, outputFile); err != nil {
		os.Remove(tmp)
		return errors.Wrap(err, "renaming temp file")
	}
	return nil
}
