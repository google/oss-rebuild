// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"google.golang.org/genai"
)

func TestRankProminence(t *testing.T) {
	got := rankProminence([]signals.ProminenceRecord{
		{Ecosystem: "npm", Package: "obscure", Rating: 0.1},
		{Ecosystem: "cratesio", Package: "ripgrep", Rating: 0.9},
		{Ecosystem: "npm", Package: "lodash", Rating: 1.0},
		{Ecosystem: "npm", Package: "", Rating: 0.5},
	})
	if len(got) != 3 {
		t.Fatalf("got %d ranked, want 3 (records missing a package are dropped)", len(got))
	}
	if got[0].Package != "ripgrep" {
		t.Errorf("ecosystems should be emitted in sorted order, got %q first", got[0].Package)
	}
	// ripgrep tops cratesio at 0.9 since quantiles are per ecosystem.
	if got[0].Prominence != 1.0 {
		t.Errorf("cratesio top quantile = %v, want 1", got[0].Prominence)
	}
	if got[1].Package != "lodash" || got[1].Prominence != 1.0 {
		t.Errorf("npm top = %q at %v, want lodash at 1", got[1].Package, got[1].Prominence)
	}
	if got[2].Prominence != 0.5 {
		t.Errorf("npm second quantile = %v, want 0.5", got[2].Prominence)
	}
}

func TestRankProminenceSharesTies(t *testing.T) {
	got := map[string]float64{}
	for _, r := range rankProminence([]signals.ProminenceRecord{
		{Ecosystem: "npm", Package: "a", Rating: 0.5},
		{Ecosystem: "npm", Package: "top", Rating: 0.9},
		{Ecosystem: "npm", Package: "b", Rating: 0.5},
		{Ecosystem: "npm", Package: "c", Rating: 0.5},
	}) {
		got[r.Package] = r.Prominence
	}
	// The 0.5 group holds ranks 1-3 (0.75, 0.5, 0.25) and shares their mean.
	want := map[string]float64{"top": 1, "a": 0.5, "b": 0.5, "c": 0.5}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("quantiles mismatch (-want +got):\n%s", diff)
	}
}

func TestPromptNamesEcosystemInEnglish(t *testing.T) {
	if got := promptFor([]pkgRef{{"cratesio", "serde"}}); !strings.Contains(got, "ecosystem=Rust") {
		t.Errorf("prompt should name the ecosystem in English, got:\n%s", got)
	}
	if got := ecoEnglish("unknown-eco"); got != "unknown-eco" {
		t.Errorf("unknown ecosystem should pass through, got %q", got)
	}
}

func TestPromptNamesSchemaKey(t *testing.T) {
	key := prominenceSchema.Items.Required[0]
	if got := promptFor([]pkgRef{{"npm", "lodash"}}); !strings.Contains(got, strconv.Quote(key)) {
		t.Errorf("prompt should ask for the schema's %q key, got:\n%s", key, got)
	}
}

// replyTransport answers every generateContent request with one model reply.
type replyTransport string

func (t replyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	text, _ := json.Marshal(string(t))
	body := `{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(text) + `}]},"finishReason":"STOP"}]}`
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestElicitFailsMisalignedBatch(t *testing.T) {
	items := []pkgRef{{"npm", "lodash"}, {"npm", "left-pad"}}
	nan := math.NaN()
	for _, tc := range []struct {
		name  string
		reply string
		want  []float64
	}{
		{"Aligned", `[{"rating":9},{"rating":3}]`, []float64{0.9, 0.3}},
		// A dropped item would shift every later rating onto the wrong package.
		{"Short", `[{"rating":9}]`, []float64{nan, nan}},
		{"Long", `[{"rating":9},{"rating":3},{"rating":5}]`, []float64{nan, nan}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: replyTransport(tc.reply)}})
			if err != nil {
				t.Fatal(err)
			}
			got := elicit(ctx, client, "m", prominenceGenConfig(), items, len(items), io.Discard)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateNaNs()); diff != "" {
				t.Errorf("ratings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadCorpus(t *testing.T) {
	lodash := pkgRef{"npm", "lodash"}
	for _, tc := range []struct {
		name string
		rows string
		want map[pkgRef]string
	}{
		{
			// A prevalence export dates only its top versions, so the package
			// stays undated.
			name: "PrevalenceExport",
			rows: `{"ecosystem":"npm","package":"lodash","dependents":9000}
{"ecosystem":"npm","package":"lodash","version":"4.17.21","published":"2021-02-20T15:42:16Z"}
{"ecosystem":"npm","package":"lodash","version":"4.17.4","published":"2016-12-31T22:33:56Z"}`,
			want: map[pkgRef]string{},
		},
		{
			name: "RegisteredRow",
			rows: `{"ecosystem":"npm","package":"lodash","registered":"2012-04-23"}`,
			want: map[pkgRef]string{lodash: "2012-04-23"},
		},
		{
			name: "Undated",
			rows: `{"ecosystem":"npm","package":"lodash","dependents":9000}`,
			want: map[pkgRef]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := memfs.New()
			if err := util.WriteFile(files, "corpus.jsonl", []byte(tc.rows+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			pairs, registered, err := loadCorpus(files, "corpus.jsonl")
			if err != nil {
				t.Fatalf("loadCorpus: %v", err)
			}
			if diff := cmp.Diff([]pkgRef{lodash}, pairs, cmp.AllowUnexported(pkgRef{})); diff != "" {
				t.Errorf("pairs mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, registered, cmp.AllowUnexported(pkgRef{})); diff != "" {
				t.Errorf("registration dates mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHorizonNeedsDatedCorpus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    string
		wantErr bool
	}{
		{"Registered", `{"ecosystem":"npm","package":"lodash","registered":"2012-04-23"}`, false},
		// A prevalence export dates only its top versions, not the package.
		{"VersionDatesOnly", `{"ecosystem":"npm","package":"lodash","version":"4.17.4","published":"2016-12-31T22:33:56Z"}`, true},
		{"Undated", `{"ecosystem":"npm","package":"lodash","dependents":9000}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			corpus, cache := filepath.Join(dir, "corpus.jsonl"), filepath.Join(dir, "cache.json")
			if err := os.WriteFile(corpus, []byte(tc.rows+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The cached rating keeps the run from reaching the model.
			cached, err := json.Marshal(map[string]float64{cacheKey("m", pkgRef{"npm", "lodash"}): 0.9})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, cached, 0o644); err != nil {
				t.Fatal(err)
			}
			deps := &Deps{IO: cli.IO{Out: io.Discard, Err: io.Discard}, FSFor: billyx.NewResolver().FS}
			cfg := prominenceConfig{Project: "p", Model: "m", Corpus: corpus, Cache: cache, Horizon: "2025-06-01", Out: filepath.Join(dir, "out.jsonl")}
			if _, err := prominenceHandler(context.Background(), cfg, deps); (err != nil) != tc.wantErr {
				t.Errorf("prominenceHandler error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
