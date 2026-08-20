// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	_ "embed"
	"html/template"
	"io"
	"net/http"
	"regexp"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

var (
	//go:embed header.gohtml
	headerHTML string
	//go:embed index.gohtml
	indexHTML string
	//go:embed package.gohtml
	packageHTML string
	//go:embed version.gohtml
	versionHTML string
	//go:embed attempt.gohtml
	attemptHTML string
	//go:embed logs.gohtml
	logsHTML string
	//go:embed dashboard.css
	css string
	//go:embed theme.css
	themeCSS string
)

// Hardcoded by the page templates, so the package owns the routes.
const (
	ThemeCSSPath = "/theme.css"
	CSSPath      = "/dashboard.css"
)

// RegisterAssets serves the dashboard's stylesheets at the paths above.
func RegisterAssets(mux *http.ServeMux) {
	for path, content := range map[string]string{ThemeCSSPath: themeCSS, CSSPath: css} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			_, _ = io.WriteString(w, content)
		})
	}
}

var (
	IndexTmpl   *template.Template
	PackageTmpl *template.Template
	VersionTmpl *template.Template
	AttemptTmpl *template.Template
	LogsTmpl    *template.Template
)

func init() {
	IndexTmpl = template.Must(template.New("index").Parse(headerHTML + indexHTML))
	PackageTmpl = template.Must(template.New("package").Parse(headerHTML + packageHTML))
	VersionTmpl = template.Must(template.New("version").Parse(headerHTML + versionHTML))
	AttemptTmpl = template.Must(template.New("attempt").Parse(headerHTML + attemptHTML))
	LogsTmpl = template.Must(template.New("logs").Parse(logsHTML))
}

var packagePathEncoding = rebuild.FilesystemTargetEncoding

type Deps struct {
	Rundex        rundex.Reader
	Sessions      rundex.SessionReader
	GCSClient     *storage.Client
	LogsBucket    string
	Tracked       feed.TrackedPackageIndex
	BenchmarkName string
	SuccessRegex  *regexp.Regexp
	Registry      rebuild.RegistryMux // enumerates published versions for status
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

// SessionView pairs an agent session with its encoded target for building
// dashboard links (e.g. to the package page).
type SessionView struct {
	schema.AgentSession
	Encoded    rebuild.EncodedTarget
	Iterations []schema.AgentIteration // NOTE: Only populated by pages where it's required
}

func NewSessionView(s schema.AgentSession) SessionView {
	return SessionView{
		AgentSession: s,
		Encoded:      packagePathEncoding.Encode(s.Target),
	}
}
