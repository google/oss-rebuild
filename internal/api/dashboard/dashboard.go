// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
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
	EncodedPackage  string
	EncodedVersion  string
	EncodedArtifact string
}

func NewRebuildView(rb rundex.Rebuild) RebuildView {
	et := rebuild.FilesystemTargetEncoding.Encode(rb.Target())
	return RebuildView{
		Rebuild:         rb,
		EncodedPackage:  et.Package,
		EncodedVersion:  et.Version,
		EncodedArtifact: et.Artifact,
	}
}

type DashboardData struct {
	RecentRebuilds   []RebuildView
	BenchmarkTitle   string
	BenchmarkStats   BenchmarkStats
	BenchmarkTargets []BenchmarkTargetView
	PackageName      string
	Ecosystem        string
	EncodedPackage   string
	PackageRebuilds  []RebuildView
	Attempt          *RebuildView
	AttemptStrategy  string
	AttemptDuration  string
}

type BenchmarkStats struct {
	Total   int
	Success int
	Failed  int
}

type BenchmarkTarget struct {
	PackageName string
	Ecosystem   string
	Success     bool
	HasRun      bool
}

type BenchmarkTargetView struct {
	BenchmarkTarget
	EncodedPackage string
}

type IndexRequest struct{}

func (IndexRequest) Validate() error { return nil }

func Index(ctx context.Context, _ IndexRequest, deps *Deps) (*DashboardData, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var rebuilds []rundex.Rebuild
	var err error
	data := DashboardData{}

	if deps.Benchmark != nil {
		// Fetch recent rebuilds filtered by benchmark
		rebuilds, err = deps.Rundex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{
			Bench: deps.Benchmark,
			Limit: 100,
		})
		if err != nil {
			return nil, errors.Wrap(err, "fetching benchmark filtered rebuilds")
		}

		// Compile benchmark stats
		tracked := make(feed.TrackedPackageIndex)
		for _, p := range deps.Benchmark.Packages {
			eco := rebuild.Ecosystem(p.Ecosystem)
			if _, ok := tracked[eco]; !ok {
				tracked[eco] = make(map[string]bool)
			}
			tracked[eco][p.Name] = true
		}

		benchRebuilds, err := deps.Rundex.LatestTrackedPackages(ctx, tracked)
		if err != nil {
			return nil, errors.Wrap(err, "fetching benchmark rebuilds")
		}

		applySuccessRegex(deps.SuccessRegex, benchRebuilds)

		successMap := make(map[string]bool)
		for _, rb := range benchRebuilds {
			key := rb.Ecosystem + ":" + rb.Package
			successMap[key] = rb.Success
		}

		var targets []BenchmarkTargetView
		stats := BenchmarkStats{Total: deps.Benchmark.Count}

		for _, p := range deps.Benchmark.Packages {
			key := p.Ecosystem + ":" + p.Name
			success, hasRun := successMap[key]
			if hasRun && success {
				stats.Success++
			} else if hasRun && !success {
				stats.Failed++
			}

			et := rebuild.FilesystemTargetEncoding.Encode(rebuild.Target{
				Ecosystem: rebuild.Ecosystem(p.Ecosystem),
				Package:   p.Name,
			})

			targets = append(targets, BenchmarkTargetView{
				BenchmarkTarget: BenchmarkTarget{
					PackageName: p.Name,
					Ecosystem:   p.Ecosystem,
					Success:     success,
					HasRun:      hasRun,
				},
				EncodedPackage: et.Package,
			})
		}

		slices.SortFunc(targets, func(a, b BenchmarkTargetView) int {
			if a.Ecosystem != b.Ecosystem {
				return strings.Compare(a.Ecosystem, b.Ecosystem)
			}
			return strings.Compare(a.PackageName, b.PackageName)
		})

		data.BenchmarkTitle = deps.BenchmarkName
		data.BenchmarkStats = stats
		data.BenchmarkTargets = targets
	} else {
		// Fetch recent global rebuild attempts.
		rebuilds, err = deps.Rundex.RecentRebuilds(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "fetching rebuilds")
		}
	}

	applySuccessRegex(deps.SuccessRegex, rebuilds)

	// Just take latest 100 to avoid huge UI
	if len(rebuilds) > 100 {
		rebuilds = rebuilds[:100]
	}

	data.RecentRebuilds = make([]RebuildView, len(rebuilds))
	for i, rb := range rebuilds {
		data.RecentRebuilds[i] = NewRebuildView(rb)
	}
	return &data, nil
}

type PackageRequest struct {
	Ecosystem string
	Package   string
}

func (PackageRequest) Validate() error { return nil }

func Package(ctx context.Context, req PackageRequest, deps *Deps) (*DashboardData, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Fetch rebuild attempts for this specific package.
	rebuilds, err := deps.Rundex.RecentPackageRebuilds(ctx, rebuild.Ecosystem(req.Ecosystem), req.Package)
	if err != nil {
		return nil, errors.Wrap(err, "fetching rebuilds")
	}

	applySuccessRegex(deps.SuccessRegex, rebuilds)

	// Sort rebuilds by creation time descending (most recent first)
	slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
		return b.Created.Compare(a.Created)
	})

	views := make([]RebuildView, len(rebuilds))
	for i, rb := range rebuilds {
		views[i] = NewRebuildView(rb)
	}

	et := rebuild.FilesystemTargetEncoding.Encode(rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
	})

	data := DashboardData{
		Ecosystem:       req.Ecosystem,
		PackageName:     req.Package,
		EncodedPackage:  et.Package,
		PackageRebuilds: views,
	}
	return &data, nil
}

type AttemptRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string `form:"runid"`
}

func (AttemptRequest) Validate() error { return nil }

func Attempt(ctx context.Context, req AttemptRequest, deps *Deps) (*DashboardData, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, errors.Wrap(err, "fetching attempt")
	}

	attempts := []rundex.Rebuild{attempt}
	applySuccessRegex(deps.SuccessRegex, attempts)
	attempt = attempts[0]

	strategyBytes, _ := json.MarshalIndent(attempt.Strategy, "", "  ")

	durationStr := "N/A"
	if !attempt.Started.IsZero() && !attempt.Created.IsZero() {
		durationStr = attempt.Created.Sub(attempt.Started).Round(time.Second).String()
	}

	view := NewRebuildView(attempt)
	et := rebuild.FilesystemTargetEncoding.Encode(rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
	})

	data := DashboardData{
		Ecosystem:       req.Ecosystem,
		PackageName:     req.Package,
		EncodedPackage:  et.Package,
		Attempt:         &view,
		AttemptStrategy: string(strategyBytes),
		AttemptDuration: durationStr,
	}
	return &data, nil
}

type LogsRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string
}

func (LogsRequest) Validate() error { return nil }

type LogsView struct {
	Ecosystem       string
	PackageName     string
	Version         string
	Artifact        string
	RunID           string
	EncodedPackage  string
	EncodedVersion  string
	EncodedArtifact string
	Logs            string
}

func Logs(ctx context.Context, req LogsRequest, deps *Deps) (*LogsView, error) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		return nil, errors.New("Log viewing is not configured")
	}

	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, errors.Wrap(err, "fetching attempt")
	}

	logID := attempt.BuildID
	if logID == "" {
		logID = attempt.ObliviousID
	}
	if logID == "" {
		return nil, errors.New("no logs available for this attempt")
	}

	obj := deps.GCSClient.Bucket(deps.LogsBucket).Object(gcb.MergedLogFile(logID))
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetching logs")
	}
	defer reader.Close()

	b, err := io.ReadAll(reader)
	if err != nil {
		return nil, errors.Wrap(err, "reading logs")
	}

	et := rebuild.FilesystemTargetEncoding.Encode(attempt.Target())

	return &LogsView{
		Ecosystem:       req.Ecosystem,
		PackageName:     req.Package,
		Version:         req.Version,
		Artifact:        req.Artifact,
		RunID:           req.RunID,
		EncodedPackage:  et.Package,
		EncodedVersion:  et.Version,
		EncodedArtifact: et.Artifact,
		Logs:            string(b),
	}, nil
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
