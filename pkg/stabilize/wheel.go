// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/oss-rebuild/internal/glob"
	"github.com/google/oss-rebuild/pkg/archive"
)

// AllWheelStabilizers is the list of all available wheel stabilizers.
var AllWheelStabilizers = []Stabilizer{
	StableWheelCargoSBOM,
	StableWheelRecord,
}

// wheelSBOMPat matches the PEP 770 SBOM documents in a wheel.
var wheelSBOMPat = regexp.MustCompile(`^[^/]+\.dist-info/sboms/[^/]+\.json$`)

// fileDownloadPat matches the path in a package URL's file download qualifier.
var fileDownloadPat = regexp.MustCompile(`(download_url=file://)[^&#]*`)

// StableWheelCargoSBOM removes the build metadata that cargo-cyclonedx records in
// the SBOMs that maturin puts in a wheel: the random serial number, the
// timestamp, and the checkout path in package ID specs and file download URLs.
var StableWheelCargoSBOM = Stabilizer{
	Name: "wheel-cargo-sbom",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	if !wheelSBOMPat.MatchString(zf.Name) {
		return
	}
	r, err := zf.Open()
	if err != nil {
		return
	}
	b, err := io.ReadAll(r)
	if err != nil || !utf8.Valid(b) {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return
	}
	meta, _ := doc["metadata"].(map[string]any)
	if !madeByCargoCycloneDX(meta) {
		return
	}
	delete(doc, "serialNumber")
	delete(meta, "timestamp")
	stripCheckoutPaths(doc)
	if out, err := json.Marshal(doc); err == nil {
		zf.SetContent(out)
	}
}))

// madeByCargoCycloneDX reports whether an SBOM's tools include cargo-cyclonedx.
func madeByCargoCycloneDX(meta map[string]any) bool {
	tools, _ := meta["tools"].([]any)
	return slices.ContainsFunc(tools, func(tool any) bool {
		t, _ := tool.(map[string]any)
		return t["name"] == "cargo-cyclonedx"
	})
}

// stripCheckoutPaths removes the checkout path from the package ID specs and
// file download URLs in v, which are the only strings that carry it.
func stripCheckoutPaths(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			v[k] = stripCheckoutPaths(e)
		}
	case []any:
		for i, e := range v {
			v[i] = stripCheckoutPaths(e)
		}
	case string:
		return fileDownloadPat.ReplaceAllString(stripSpecPath(v), "$1")
	}
	return v
}

// stripSpecPath removes the checkout path from a Cargo path+file package ID
// spec. Cargo omits the crate name when it matches the checkout directory's
// name, so the name is restored from the path first.
func stripSpecPath(spec string) string {
	rest, ok := strings.CutPrefix(spec, "path+file://")
	dir, frag, found := strings.Cut(rest, "#")
	if !ok || !found {
		return spec
	}
	if !strings.Contains(frag, "@") {
		frag = path.Base(dir) + "@" + frag
	}
	return "path+file://#" + frag
}

// StableWheelRecord canonicalizes the line order of a wheel's *.dist-info/RECORD file.
// Per PEP 376 (and 428), the RECORD file lists every other entry as
// `path,<digest-type>=<urlsafe-b64-digest>,size` and itself last as
// `<dist-info>/RECORD,,`.
//
// NOTE: Pre-`bdist_wheel 0.36` tooling ordered the lines in fs walk order
// which is unstable so we sort the entries lexicographically by path.
//
// NOTE: Runs at StageFinalize as any earlier stabilizer that altered an entry's
// content or membership would render the RECORD inaccurate. This is notably
// the case for custom stabilizers which often exclude paths from the package.
var StableWheelRecord = Stabilizer{
	Name: "wheel-record",
}.WithFn(ZipArchiveFn(func(mr *archive.MutableZipReader) {
	var record *archive.MutableZipFile
	for _, f := range mr.File {
		if match, err := glob.Match("*.dist-info/RECORD", f.Name); err == nil && match {
			record = f
			break
		}
	}
	if record == nil {
		return
	}
	type rec struct {
		path, digest string
		size         int64
	}
	var entries []rec
	for _, f := range mr.File {
		if f == record {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return
		}
		h := sha256.New()
		n, err := io.Copy(h, r)
		if err != nil {
			return
		}
		entries = append(entries, rec{
			path:   f.Name,
			digest: "sha256=" + base64.RawURLEncoding.EncodeToString(h.Sum(nil)),
			size:   n,
		})
	}
	slices.SortStableFunc(entries, func(a, b rec) int { return strings.Compare(a.path, b.path) })
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s,%s,%d\n", csvQuote(e.path), e.digest, e.size)
	}
	fmt.Fprintf(&buf, "%s,,\n", csvQuote(record.Name))
	record.SetContent(buf.Bytes())
})).WithOrdering(StageFinalize)

// csvQuote returns s minimally quoted per PEP 376.
func csvQuote(s string) string {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
