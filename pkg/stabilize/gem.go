// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"slices"

	"github.com/google/oss-rebuild/pkg/archive"
)

// AllGemStabilizers is the list of all available gem stabilizers.
var AllGemStabilizers = []Stabilizer{
	StableGemExcludeChecksums,
	StableGemInnerArchives,
}

// StableGemExcludeChecksums removes checksums.yaml.gz since its content
// references pre-stabilization archive hashes.
// Constrained to depth 0 (the outermost .gem tar).
var StableGemExcludeChecksums = Stabilizer{
	Name: "gem-exclude-checksums",
}.WithConstraints(AtDepth(0)).WithFn(TarArchiveFn(func(ta *archive.TarArchive) {
	ta.Files = slices.DeleteFunc(ta.Files, func(e *archive.TarEntry) bool {
		return e.Name == "checksums.yaml.gz"
	})
}))

// StableGemInnerArchives recursively stabilizes the inner archives within a gem.
// Constrained to depth 0 (the outermost .gem tar) to avoid applying to nested archives.
var StableGemInnerArchives = Stabilizer{
	Name: "gem-inner-archives",
}.WithConstraints(AtDepth(0)).WithFn(TarEntryContextFn(func(entry *archive.TarEntry, ctx *StabilizationContext) {
	var innerFormat archive.Format
	switch entry.Name {
	case "data.tar.gz":
		innerFormat = archive.TarGzFormat
	case "metadata.gz":
		innerFormat = archive.GzipFormat
	default:
		return
	}
	nestedCtx := ctx.WithNestedArchive(innerFormat, entry.Name)
	var buf bytes.Buffer
	if err := stabilizeWithCtx(&buf, bytes.NewReader(entry.Body), innerFormat, nestedCtx); err != nil {
		return
	}
	entry.Body = buf.Bytes()
	entry.Size = int64(len(entry.Body))
}))
