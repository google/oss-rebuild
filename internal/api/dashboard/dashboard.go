// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

var (
	//go:embed layout.gohtml
	layoutHTML string
	//go:embed index.gohtml
	indexHTML string
	//go:embed package.gohtml
	packageHTML string
	//go:embed version.gohtml
	versionHTML string
	//go:embed attempt.gohtml
	attemptHTML string
	//go:embed session.gohtml
	sessionHTML string
	//go:embed transcript.gohtml
	transcriptHTML string
	//go:embed packages.gohtml
	packagesHTML string
	//go:embed resources.gohtml
	resourcesHTML string
	//go:embed coverage.gohtml
	coverageHTML string
	//go:embed dashboard.css
	css string
	//go:embed theme.css
	themeCSS string
	//go:embed favicon.svg
	favicon string
	//go:embed charts.js
	chartsJS string
	//go:embed dashboard.js
	dashboardJS string
	//go:embed viz.gohtml
	vizHTML string
	//go:embed rows.gohtml
	rowsHTML string
)

// Hardcoded by the page templates, so the package owns the routes.
const (
	ThemeCSSPath    = "/theme.css"
	CSSPath         = "/dashboard.css"
	FaviconPath     = "/favicon.svg"
	ChartsJSPath    = "/charts.js"
	DashboardJSPath = "/dashboard.js"
)

// ContentSecurityPolicy locks the read-only dashboard to same-origin assets
// with no forms, framing, or plugins. Templates use inline style attributes
// and style blocks, so style-src allows 'unsafe-inline'.
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

// SecurityHeaders sets CSP and standard hardening headers on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// asset is a file the pages link, with a digest of its content that the
// links carry.
type asset struct{ content, version string }

func newAsset(content string) asset {
	sum := sha256.Sum256([]byte(content))
	return asset{content, hex.EncodeToString(sum[:6])}
}

// assets are the stylesheets, scripts, and icon the pages link, by route.
var assets = map[string]asset{
	ThemeCSSPath:    newAsset(themeCSS),
	CSSPath:         newAsset(css),
	FaviconPath:     newAsset(favicon),
	ChartsJSPath:    newAsset(chartsJS),
	DashboardJSPath: newAsset(dashboardJS),
}

// assetURL links the asset at route by its version.
func assetURL(route string) string {
	if a, ok := assets[route]; ok {
		return route + "?v=" + a.version
	}
	return route
}

// assetFuncs let templates link assets.
var assetFuncs = template.FuncMap{"asset": assetURL}

// RegisterAssets serves the dashboard's stylesheets, script, and icon, and
// under the webfonts build tag the embedded font files. A request for an
// asset at its current version may be cached indefinitely, since new content
// gets a new link. Conditional requests with If-None-Match receive 304.
func RegisterAssets(mux *http.ServeMux) {
	for route, a := range assets {
		etag := `"` + a.version + `"`
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(route)))
			w.Header().Set("ETag", etag)
			if r.URL.Query().Get("v") == a.version {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeContent(w, r, route, time.Time{}, strings.NewReader(a.content))
		})
	}
	registerFonts(mux)
}

var (
	IndexTmpl   *template.Template
	PackageTmpl *template.Template
	VersionTmpl *template.Template
	AttemptTmpl *template.Template
	SessionTmpl *template.Template
	// TranscriptTmpl renders a session's full transcript and exec ledger,
	// from the same data as SessionTmpl.
	TranscriptTmpl *template.Template
	PackagesTmpl   *template.Template
	ResourcesTmpl  *template.Template
	CoverageTmpl   *template.Template
)

func init() {
	base := template.Must(template.New("page").Funcs(pageFuncs).Funcs(assetFuncs).Parse(layoutHTML))
	// The webfonts build tag defines the template with preload links and a
	// font stylesheet; without it the definition is empty and pages render
	// with system fonts.
	template.Must(base.Parse(webfontsTmpl))
	template.Must(base.Parse(vizHTML))
	template.Must(base.Parse(rowsHTML))
	page := func(src string) *template.Template {
		return template.Must(template.Must(base.Clone()).Parse(src))
	}
	IndexTmpl = page(indexHTML)
	PackageTmpl = page(packageHTML)
	VersionTmpl = page(versionHTML)
	AttemptTmpl = page(attemptHTML)
	SessionTmpl = page(sessionHTML)
	TranscriptTmpl = page(transcriptHTML)
	PackagesTmpl = page(packagesHTML)
	ResourcesTmpl = page(resourcesHTML)
	CoverageTmpl = page(coverageHTML)
}

//go:generate go run fonts/fetch_fonts.go

var packagePathEncoding = rebuild.FilesystemTargetEncoding

type Deps struct {
	Rundex        rundex.Reader
	Sessions      rundex.SessionReader
	GCSClient     *storage.Client
	LogsBucket    string
	Benchmark     rundex.PackageIndex
	BenchmarkName string
	SuccessRegex  *regexp.Regexp
	Analytics     Analytics           // serves the snapshot-derived trend views
	Registry      rebuild.RegistryMux // enumerates published versions for status

	SessionsBucket string          // agent chat transcripts, read by the session page
	Execs          db.ScratchExecs // the session page's exec ledger
}

type RebuildView struct {
	rundex.Rebuild
	Encoded rebuild.EncodedTarget
}

func NewRebuildView(rb rundex.Rebuild) RebuildView {
	rb.Message = simplifyMessage(rb.Message)
	et := packagePathEncoding.Encode(rb.Target())
	return RebuildView{
		Rebuild: rb,
		Encoded: et,
	}
}

// Tone is the attempt's outcome as a status class: ok, bad, or live.
func (v RebuildView) Tone() string {
	switch {
	case v.Success:
		return "ok"
	case v.Status == schema.RebuildStatusRunning:
		return "live"
	}
	return "bad"
}

// Outcome is Tone in words.
func (v RebuildView) Outcome() string {
	switch v.Tone() {
	case "ok":
		return "reproduced"
	case "live":
		return "running"
	}
	return "failed"
}

// Href is the attempt's page.
func (v RebuildView) Href() string {
	return fmt.Sprintf("/attempt/%s/%s/%s/%s/%s", v.Encoded.Ecosystem, v.Encoded.Package, v.Encoded.Version, v.Encoded.Artifact, v.RunID)
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
	if s.StopReason != schema.AgentCompleteReasonGaveUp {
		s.Summary = simplifyMessage(s.Summary)
	}
	return SessionView{
		AgentSession: s,
		Encoded:      packagePathEncoding.Encode(s.Target),
	}
}

// Outcome is how the session ended, or its status while it runs.
func (v SessionView) Outcome() string {
	switch v.StopReason {
	case "":
		return strings.ToLower(v.Status)
	case schema.AgentCompleteReasonSuccess:
		return "reproduced"
	case schema.AgentCompleteReasonGaveUp:
		return "gave up"
	}
	return strings.ToLower(v.StopReason)
}

// Tone is Outcome as the status color classes name it.
func (v SessionView) Tone() string {
	switch v.StopReason {
	case "":
		return "live"
	case schema.AgentCompleteReasonSuccess:
		return "ok"
	case schema.AgentCompleteReasonGaveUp:
		return "warn"
	}
	return "bad"
}

// Verdict sorts how the session ended: reproduced, gave up, timed out, hit
// a resource limit, or ended in an error. Failures split on the summaries
// internal/agent and the reaper write.
func (v SessionView) Verdict() string {
	switch s := v.Summary; {
	case v.StopReason == "":
		return "running"
	case v.StopReason == schema.AgentCompleteReasonSuccess:
		return "reproduced"
	case v.StopReason == schema.AgentCompleteReasonGaveUp:
		return "gave up"
	case v.StopReason == schema.AgentCompleteReasonThrottled, strings.HasPrefix(s, "Maximum iterations"), strings.Contains(s, "ResourceExhausted"):
		return "resource limit"
	case strings.HasPrefix(s, "Iteration budget"), strings.Contains(s, "inference ran past"), strings.Contains(s, "exceeded its timeout"):
		return "timed out"
	}
	return "error"
}

// Reason is the model's own words for a give-up or the error behind a failure.
func (v SessionView) Reason() string {
	switch v.StopReason {
	case "", schema.AgentCompleteReasonSuccess:
		return ""
	case schema.AgentCompleteReasonGaveUp:
		r := strings.TrimPrefix(v.Summary, "Gave up: ")
		return r
	}
	return failureCause(v.Summary)
}
