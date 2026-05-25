// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/internal/glob"
	"github.com/google/oss-rebuild/pkg/archive"
)

// AllWheelStabilizers is the list of all available wheel stabilizers.
var AllWheelStabilizers = []Stabilizer{
	StableWheelRecord,
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
