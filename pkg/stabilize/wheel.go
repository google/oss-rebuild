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
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/oss-rebuild/internal/glob"
	"github.com/google/oss-rebuild/pkg/archive"
)

// AllWheelStabilizers is the list of all available wheel stabilizers.
var AllWheelStabilizers = []Stabilizer{
	StableWheelCycloneDXSBOM,
	StableWheelRecord,
}

var (
	// PEP 770 places wheel SBOM documents under <dist-info>/sboms/.
	wheelSBOMPathRe = regexp.MustCompile(`^[^/]+\.dist-info/sboms/.*\.json$`)
	// cargo-cyclonedx records the absolute build checkout path in the purl download_url qualifier. Relative file://. URLs are deterministic and kept.
	purlAbsFileDownloadRe = regexp.MustCompile(`([?&]download_url=file:///)[^&#?]*`)
)

// StableWheelCycloneDXSBOM normalizes volatile build-time metadata in PEP 770
// CycloneDX SBOM JSON files while preserving the component and dependency
// inventory.
// NOTE: Runs at StageDefault before StableWheelRecord at StageFinalize so the
// RECORD digest and byte length reflect the normalized SBOM bytes. Parse
// errors and non-CycloneDX documents leave the entry untouched.
var StableWheelCycloneDXSBOM = Stabilizer{
	Name: "wheel-cyclonedx-sbom",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	if !wheelSBOMPathRe.MatchString(zf.Name) {
		return
	}
	r, err := zf.Open()
	if err != nil {
		return
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return
	}
	doc, ok := decodeCycloneDXObject(raw)
	if !ok {
		return
	}
	delete(doc, "serialNumber")
	if meta, ok := doc["metadata"].(map[string]any); ok {
		delete(meta, "timestamp")
	}
	normalizeCycloneDXMap(doc)
	normalized, err := json.Marshal(doc)
	if err != nil {
		return
	}
	zf.SetContent(normalized)
})).WithOrdering(StageDefault)

// decodeCycloneDXObject parses a CycloneDX JSON object while rejecting inputs
// that would lose information on re-encoding, such as invalid UTF-8, lone
// surrogate escapes replaced with U+FFFD, or duplicate object keys.
func decodeCycloneDXObject(raw []byte) (map[string]any, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, ok := decodeStrictJSONValue(dec)
	if !ok {
		return nil, false
	}
	doc, ok := v.(map[string]any)
	if !ok || doc["bomFormat"] != "CycloneDX" {
		return nil, false
	}
	return doc, true
}

func decodeStrictJSONValue(dec *json.Decoder) (any, bool) {
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			m := make(map[string]any)
			for dec.More() {
				kTok, err := dec.Token()
				k, ok := kTok.(string)
				if err != nil || !ok || strings.ContainsRune(k, utf8.RuneError) {
					return nil, false
				}
				if _, dup := m[k]; dup {
					return nil, false
				}
				val, ok := decodeStrictJSONValue(dec)
				if !ok {
					return nil, false
				}
				m[k] = val
			}
			if _, err := dec.Token(); err != nil {
				return nil, false
			}
			return m, true
		case '[':
			arr := []any{}
			for dec.More() {
				val, ok := decodeStrictJSONValue(dec)
				if !ok {
					return nil, false
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, false
			}
			return arr, true
		}
		return nil, false
	case string:
		if strings.ContainsRune(v, utf8.RuneError) {
			return nil, false
		}
		return v, true
	default:
		return v, true
	}
}

// normalizeCycloneDXMap rewrites, at any depth, the CycloneDX fields that carry
// component references (bom-ref, ref, dependsOn, and purl). Keys are matched by
// name alone because CycloneDX reserves these field names for references and
// package URLs across the document tree.
func normalizeCycloneDXMap(m map[string]any) {
	for k, v := range m {
		switch k {
		case "bom-ref", "ref":
			if s, ok := v.(string); ok {
				m[k] = stripCargoPathSpecHost(s)
			}
		case "purl":
			if s, ok := v.(string); ok {
				m[k] = purlAbsFileDownloadRe.ReplaceAllString(s, "${1}")
			}
		case "dependsOn":
			if arr, ok := v.([]any); ok {
				for i, elem := range arr {
					if s, ok := elem.(string); ok {
						arr[i] = stripCargoPathSpecHost(s)
					}
				}
			}
		}
		switch sub := m[k].(type) {
		case map[string]any:
			normalizeCycloneDXMap(sub)
		case []any:
			normalizeCycloneDXSlice(sub)
		}
	}
}

func normalizeCycloneDXSlice(arr []any) {
	for _, elem := range arr {
		switch sub := elem.(type) {
		case map[string]any:
			normalizeCycloneDXMap(sub)
		case []any:
			normalizeCycloneDXSlice(sub)
		}
	}
}

// stripCargoPathSpecHost strips the build-host checkout path from Cargo
// path+file PackageIdSpec references while retaining the crate name, version,
// and target suffix. When Cargo omitted "<name>@" from the fragment because
// the directory basename matched the crate name, the basename is restored so
// builds from "/src" and "/home/runner/work/<repo>/<crate>" normalize to the
// same reference.
func stripCargoPathSpecHost(s string) string {
	rest, ok := strings.CutPrefix(s, "path+file://")
	if !ok {
		return s
	}
	rawPath, fragment, ok := strings.Cut(rest, "#")
	if !ok || fragment == "" {
		return s
	}
	if !strings.Contains(fragment, "@") {
		trimmed := strings.TrimRight(rawPath, "/")
		if idx := strings.LastIndexByte(trimmed, '/'); idx >= 0 {
			if base := trimmed[idx+1:]; !strings.HasSuffix(base, ":") {
				fragment = base + "@" + fragment
			}
		}
	}
	return "path+file:///#" + fragment
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
