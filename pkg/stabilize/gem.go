// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

// AllGemStabilizers is the list of all available gem stabilizers.
var AllGemStabilizers = []Stabilizer{
	StableGemExcludeChecksums,
	StableGemExcludeSignatures,
	StableGemMetadataDate,
	StableGemMetadataRubygemsVersion,
	StableGemMetadataCertChain,
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

// StableGemExcludeSignatures removes detached gem-signature files
// (data.tar.gz.sig, metadata.gz.sig, checksums.yaml.gz.sig). These are
// produced by `gem build --sign` and isn't reproducible by a third party.
var StableGemExcludeSignatures = Stabilizer{
	Name: "gem-exclude-signatures",
}.WithConstraints(AtDepth(0)).WithFn(TarArchiveFn(func(ta *archive.TarArchive) {
	ta.Files = slices.DeleteFunc(ta.Files, func(e *archive.TarEntry) bool {
		return strings.HasSuffix(e.Name, ".sig")
	})
}))

var gemMetadataDateRe = regexp.MustCompile(`(?m)^date: [^\r\n]+`)
var gemMetadataRubygemsVersionRe = regexp.MustCompile(`(?m)^rubygems_version: [^\r\n]+`)

// StableGemMetadataDate normalizes the date field in gem metadata YAML.
// The date is stamped by `gem build` at build time and so varies across rebuilds.
// The replacement value matches RubyGems' DEFAULT_SOURCE_DATE_EPOCH (1980-01-02 UTC):
// https://github.com/rubygems/rubygems/blob/0b469ed/lib/rubygems.rb#L151
var StableGemMetadataDate = Stabilizer{
	Name: "gem-metadata-date",
}.WithConstraints(AtDepth(1), ArchivePath("metadata.gz")).WithFn(GzipContentFn(func(b []byte) []byte {
	return gemMetadataDateRe.ReplaceAll(b, []byte("date: 1980-01-02 00:00:00.000000000 Z"))
}))

// StableGemMetadataRubygemsVersion normalizes the rubygems_version field in gem metadata YAML.
// This field records the RubyGems version used to build the
// gem and may differ between the original and rebuild environments.
var StableGemMetadataRubygemsVersion = Stabilizer{
	Name: "gem-metadata-rubygems-version",
}.WithConstraints(AtDepth(1), ArchivePath("metadata.gz")).WithFn(GzipContentFn(func(b []byte) []byte {
	return gemMetadataRubygemsVersionRe.ReplaceAll(b, []byte("rubygems_version: 0.0.0"))
}))

// gemMetadataCertChainRe matches the `cert_chain` metadata YAML block.
// NOTE: This attempts to match the block in both inline and list forms.
var gemMetadataCertChainRe = regexp.MustCompile(`(?m)^cert_chain:\n(?:[ -].*\n)+`)

// StableGemMetadataCertChain strips an embedded publisher cert_chain from the
// metadata YAML so that signed and unsigned variants of the same gem compare
// equal. See StableGemExcludeSignatures.
var StableGemMetadataCertChain = Stabilizer{
	Name: "gem-metadata-cert-chain",
}.WithConstraints(AtDepth(1), ArchivePath("metadata.gz")).WithFn(GzipContentFn(func(b []byte) []byte {
	return gemMetadataCertChainRe.ReplaceAll(b, []byte("cert_chain: []\n"))
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
