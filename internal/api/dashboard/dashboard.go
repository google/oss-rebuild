// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	_ "embed"
	"html/template"
	"regexp"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
)

var (
	//go:embed header.gohtml
	headerHTML string
	//go:embed index.gohtml
	indexHTML string
	//go:embed package.gohtml
	packageHTML string
	//go:embed attempt.gohtml
	attemptHTML string
	//go:embed logs.gohtml
	logsHTML string
)

var (
	IndexTmpl   *template.Template
	PackageTmpl *template.Template
	AttemptTmpl *template.Template
	LogsTmpl    *template.Template
)

func init() {
	IndexTmpl = template.Must(template.New("index").Parse(headerHTML + indexHTML))
	PackageTmpl = template.Must(template.New("package").Parse(headerHTML + packageHTML))
	AttemptTmpl = template.Must(template.New("attempt").Parse(headerHTML + attemptHTML))
	LogsTmpl = template.Must(template.New("logs").Parse(logsHTML))
}

var packagePathEncoding = rebuild.FilesystemTargetEncoding

type Deps struct {
	Rundex        *rundex.FirestoreClient
	GCSClient     *storage.Client
	LogsBucket    string
	Benchmark     *benchmark.PackageSet
	BenchmarkName string
	SuccessRegex  *regexp.Regexp
}

type RebuildView struct {
	rundex.Rebuild
	Encoded rebuild.EncodedTarget
}

func NewRebuildView(rb rundex.Rebuild) RebuildView {
	et := packagePathEncoding.Encode(rb.Target())
	return RebuildView{
		Rebuild: rb,
		Encoded: et,
	}
}

func applySuccessRegex(successRegex *regexp.Regexp, rebuilds []rundex.Rebuild) {
	if successRegex == nil {
		return
	}
	for i := range rebuilds {
		if !rebuilds[i].Success && successRegex.MatchString(rebuilds[i].Message) {
			rebuilds[i].Success = true
		}
	}
}
