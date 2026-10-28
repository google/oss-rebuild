// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/genai"
)

type pkgRef struct{ eco, pkg string }

// ecoName is the language name the prompt gives each ecosystem id, since the
// model places "a Rust crate" far better than "cratesio".
var ecoName = map[string]string{
	"pypi": "Python", "npm": "JavaScript", "cratesio": "Rust", "cargo": "Rust",
	"rubygems": "Ruby", "maven": "Java", "go": "Go",
}

func ecoEnglish(eco string) string {
	if n, ok := ecoName[strings.ToLower(eco)]; ok {
		return n
	}
	return eco
}

// prominenceRubric anchors each integer to a decade of popularity rank.
// Recognition is log-scaled, so a rank decade calibrates far better than a
// 0-100 score. A vague name lands at 5 and one it doubts exists at 0.
const prominenceRubric = "Rate how prominent each SOFTWARE PACKAGE is within its own ecosystem's registry, " +
	"judged strictly as the package published under that EXACT name -- not as an English word, brand, " +
	"famous person, or unrelated topic. Estimate the package's popularity RANK in its registry (rank 1 = " +
	"the single most-installed / most-depended-upon package) and map that rank to an integer 0-10 on this " +
	"log scale:\n" +
	"  10 = top ~10 packages in the ecosystem (the handful nearly everything depends on)\n" +
	"   9 = roughly top 100\n" +
	"   8 = roughly top 1,000\n" +
	"   7 = roughly top 10,000\n" +
	"   6 = roughly top 100,000\n" +
	"   5 = a real but long-tail package you can place only vaguely\n" +
	"   1-2 = you have only a faint sense a package by that exact name exists\n" +
	"   0 = you do not believe any package exists under that EXACT name\n" +
	"Each step down is about 10x more packages, so most of the registry sits at 5 or below. Do not inflate " +
	"a package for sharing a common word or a famous topic; a plausible-but-unfamiliar name is 0-2, not high."

// prominenceSchema makes a malformed reply fail loudly rather than parse into
// a plausible wrong number.
var prominenceSchema = &genai.Schema{
	Type: genai.TypeArray,
	Items: &genai.Schema{
		Type:       genai.TypeObject,
		Properties: map[string]*genai.Schema{"rating": {Type: genai.TypeInteger}},
		Required:   []string{"rating"},
	},
}

type ratingItem struct {
	Rating float64 `json:"rating"`
}

func newGenAIClient(ctx context.Context, project, location string) (*genai.Client, error) {
	if location == "" {
		location = "global"
	}
	c, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
	return c, errors.Wrap(err, "creating genai client")
}

func prominenceGenConfig() *genai.GenerateContentConfig {
	return &genai.GenerateContentConfig{
		Temperature:      new(float32(0)),
		ResponseMIMEType: llm.JSONMIMEType,
		ResponseSchema:   prominenceSchema,
	}
}

func promptFor(chunk []pkgRef) string {
	var b strings.Builder
	b.WriteString(prominenceRubric)
	b.WriteString("\n\nReturn ONLY a JSON array, one object per item in order, each {\"rating\": int}. Items:\n")
	for i, r := range chunk {
		fmt.Fprintf(&b, "%d. name=%q ecosystem=%s\n", i+1, r.pkg, ecoEnglish(r.eco))
	}
	return b.String()
}

// elicitBatch scores one chunk: a rating in [0,1] per item. Ratings match
// items by position, so a reply of any other length fails the whole chunk.
func elicitBatch(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, chunk []pkgRef) ([]float64, error) {
	var arr []ratingItem
	if err := llm.GenerateTypedContent(ctx, client, model, config, &arr, genai.NewPartFromText(promptFor(chunk))); err != nil {
		return nil, err
	}
	if len(arr) != len(chunk) {
		return nil, errors.Errorf("got %d ratings for %d packages", len(arr), len(chunk))
	}
	out := make([]float64, len(chunk))
	for i, it := range arr {
		out[i] = math.Max(0, math.Min(10, it.Rating)) / 10
	}
	return out, nil
}

// elicit scores every item by batch. A failed batch leaves NaN rather than 0,
// so an error cannot brand a real package obscure and skew the quantiles.
func elicit(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, items []pkgRef, batch int, errw io.Writer) []float64 {
	if batch <= 0 {
		batch = 15
	}
	out := make([]float64, len(items))
	for i := range out {
		out[i] = math.NaN()
	}
	for i := 0; i < len(items); i += batch {
		end := min(i+batch, len(items))
		ps, err := elicitBatch(ctx, client, model, config, items[i:end])
		if err != nil {
			fmt.Fprintf(errw, "[%s] batch %d: %v\n", model, i, err)
			continue
		}
		copy(out[i:end], ps)
		fmt.Fprintf(errw, "  %s %d/%d\r", model, end, len(items))
	}
	fmt.Fprintln(errw)
	return out
}

// promptKey is part of the cache key, so editing the rubric or the reply
// format re-rates every package rather than mixing answers to two prompts.
var promptKey = fmt.Sprintf("%x", sha256.Sum256([]byte(promptFor(nil))))[:8]

func cacheKey(model string, r pkgRef) string {
	return model + "|" + promptKey + "|" + r.eco + "|" + r.pkg
}

// corpusRecord is one row of the scoring corpus, such as a prevalence export.
// Its dates feed the horizon rule, compared as 10-character ISO strings.
type corpusRecord struct {
	Ecosystem      string `json:"ecosystem"`
	Package        string `json:"package"`
	Version        string `json:"version"` // set on a version row, whose dates are not the package's
	Registered     string `json:"registered"`
	Created        string `json:"created"`
	Published      string `json:"published"`
	FirstPublished string `json:"first_published"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isoDate(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func loadCorpus(fsys billy.Filesystem, name string) ([]pkgRef, map[pkgRef]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, nil, errors.Wrap(err, "opening corpus")
	}
	defer f.Close()
	seen := map[pkgRef]bool{}
	var pairs []pkgRef
	registered := map[pkgRef]string{}
	for r, err := range jsonl.Decode[corpusRecord](f) {
		if err != nil {
			return nil, nil, errors.Wrap(err, "reading corpus")
		}
		if r.Ecosystem == "" || r.Package == "" {
			continue
		}
		k := pkgRef{r.Ecosystem, r.Package}
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, k)
		}
		// Only a package row dates registration. A prevalence export dates just
		// its top versions, mostly recent releases of long-registered packages.
		if d := isoDate(firstNonEmpty(r.Registered, r.Created, r.Published, r.FirstPublished)); d != "" && r.Version == "" {
			registered[k] = d
		}
	}
	return pairs, registered, nil
}

func loadCache(fsys billy.Filesystem, name string) (map[string]float64, error) {
	m := map[string]float64{}
	if fsys == nil {
		return m, nil
	}
	r, err := fsys.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "reading cache")
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrap(err, "reading cache")
	}
	if len(raw) == 0 {
		return m, nil
	}
	return m, errors.Wrap(json.Unmarshal(raw, &m), "parsing cache")
}

func saveCache(fsys billy.Filesystem, name string, m map[string]float64) error {
	if fsys == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return errors.Wrap(err, "encoding cache")
	}
	w, err := fsys.Create(name)
	if err != nil {
		return errors.Wrap(err, "writing cache")
	}
	if _, err := w.Write(b); err != nil {
		w.Close()
		return errors.Wrap(err, "writing cache")
	}
	return errors.Wrap(w.Close(), "writing cache")
}

type prominenceConfig struct {
	Project  string
	Location string
	Model    string
	Corpus   string
	File     string
	Out      string
	Cache    string
	Horizon  string
	Batch    int
}

func (c prominenceConfig) Validate() error {
	if c.Corpus == "" && c.File == "" {
		return errors.New("one of corpus or file is required")
	}
	if c.Corpus != "" && c.File != "" {
		return errors.New("corpus and file are mutually exclusive")
	}
	if c.Out == "" {
		return errors.New("out is required")
	}
	if c.Project == "" {
		return errors.New("project is required")
	}
	return nil
}

func prominenceHandler(ctx context.Context, cfg prominenceConfig, deps *Deps) (*act.NoOutput, error) {
	recs, err := prominenceRecords(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	ranked := rankProminence(recs)
	outFS, out, err := deps.FSFor(ctx, cfg.Out)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving %s", cfg.Out)
	}
	w, err := outFS.Create(out)
	if err != nil {
		return nil, errors.Wrapf(err, "opening %s", cfg.Out)
	}
	if err := jsonl.Encode(w, ranked); err != nil {
		w.Close()
		return nil, errors.Wrap(err, "writing export")
	}
	if err := w.Close(); err != nil {
		return nil, errors.Wrapf(err, "finalizing %s", cfg.Out)
	}
	fmt.Fprintf(deps.IO.Out, "wrote %d prominence record(s) to %s\n", len(ranked), cfg.Out)
	return &act.NoOutput{}, nil
}

// prominenceRecords reads a scored export or scores the corpus.
func prominenceRecords(ctx context.Context, cfg prominenceConfig, deps *Deps) ([]signals.ProminenceRecord, error) {
	if cfg.File != "" {
		fileFS, name, err := deps.FSFor(ctx, cfg.File)
		if err != nil {
			return nil, errors.Wrap(err, "resolving prominence file")
		}
		f, err := fileFS.Open(name)
		if err != nil {
			return nil, errors.Wrap(err, "opening prominence file")
		}
		defer f.Close()
		recs, err := jsonl.DecodeAll[signals.ProminenceRecord](f)
		return recs, errors.Wrap(err, "reading prominence file")
	}
	corpusFS, corpus, err := deps.FSFor(ctx, cfg.Corpus)
	if err != nil {
		return nil, errors.Wrap(err, "resolving corpus")
	}
	pairs, registered, err := loadCorpus(corpusFS, corpus)
	if err != nil {
		return nil, err
	}
	// Horizon rule: the model cannot know a package registered after H, so it
	// is floored without an API call. The floor is not cached, so advancing H
	// never leaves one behind.
	horizon := isoDate(cfg.Horizon)
	floored := map[pkgRef]bool{}
	if horizon != "" {
		if len(registered) == 0 {
			return nil, errors.New("horizon needs package registration dates and the corpus has none")
		}
		for _, r := range pairs {
			if d, ok := registered[r]; ok && d > horizon {
				floored[r] = true
			}
		}
	}
	var cacheFS billy.Filesystem
	var cacheName string
	if cfg.Cache != "" {
		if cacheFS, cacheName, err = deps.FSFor(ctx, cfg.Cache); err != nil {
			return nil, errors.Wrap(err, "resolving cache")
		}
	}
	cache, err := loadCache(cacheFS, cacheName)
	if err != nil {
		return nil, err
	}
	var todo []pkgRef
	for _, r := range pairs {
		if _, cached := cache[cacheKey(cfg.Model, r)]; !floored[r] && !cached {
			todo = append(todo, r)
		}
	}
	fmt.Fprintf(deps.IO.Err, "%d package(s), %d post-horizon (floored), %d to score with %s\n",
		len(pairs), len(floored), len(todo), cfg.Model)
	if len(todo) > 0 {
		client, err := deps.GenAI(ctx, cfg.Project, cfg.Location)
		if err != nil {
			return nil, err
		}
		ps := elicit(ctx, client, cfg.Model, prominenceGenConfig(), todo, cfg.Batch, deps.IO.Err)
		for i, r := range todo {
			// A failed batch stays uncached, so the next run retries it.
			if !math.IsNaN(ps[i]) {
				cache[cacheKey(cfg.Model, r)] = ps[i]
			}
		}
		if err := saveCache(cacheFS, cacheName, cache); err != nil {
			return nil, err
		}
	}
	var recs []signals.ProminenceRecord
	for _, r := range pairs {
		switch v, ok := cache[cacheKey(cfg.Model, r)]; {
		case floored[r]:
			recs = append(recs, signals.ProminenceRecord{Ecosystem: r.eco, Package: r.pkg, Model: cfg.Model, Rating: 0})
		case ok:
			recs = append(recs, signals.ProminenceRecord{Ecosystem: r.eco, Package: r.pkg, Model: cfg.Model, Rating: v})
		}
	}
	if n := len(pairs) - len(recs); n > 0 {
		fmt.Fprintf(deps.IO.Err, "%d package(s) left unscored by failed batches, omitted\n", n)
	}
	return recs, nil
}

// rankProminence sets Prominence to Rating's quantile within each ecosystem.
// Ratings come from integers 0-10, so ties are large. A tied group shares its
// mid-rank quantile so input order cannot split it.
func rankProminence(recs []signals.ProminenceRecord) []signals.ProminenceRecord {
	byEco := map[string][]signals.ProminenceRecord{}
	for _, r := range recs {
		if r.Ecosystem != "" && r.Package != "" {
			byEco[r.Ecosystem] = append(byEco[r.Ecosystem], r)
		}
	}
	var out []signals.ProminenceRecord
	for _, eco := range slices.Sorted(maps.Keys(byEco)) {
		ranked := byEco[eco]
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Rating > ranked[j].Rating })
		n := len(ranked)
		for i := 0; i < n; {
			j := i + 1
			for j < n && ranked[j].Rating == ranked[i].Rating {
				j++
			}
			q := (signals.Percentile(i, n) + signals.Percentile(j-1, n)) / 2
			for k := i; k < j; k++ {
				ranked[k].Prominence = q
			}
			i = j
		}
		out = append(out, ranked...)
	}
	return out
}

func prominenceCommand() *cobra.Command {
	cfg := prominenceConfig{}
	cmd := &cobra.Command{
		Use:   "prominence --project <project> (--corpus <uri> | --file <uri>) --out <uri>",
		Short: "Rank packages by LLM-elicited public notability (leaf and application coverage)",
		Long: `Rank packages by LLM-elicited public notability.

Asks Gemini once per package, under a decade-anchored log-rank rubric, how well
known the package is under that exact name, then ranks the answers into
per-ecosystem quantiles and writes the result as a JSONL export.

This exists because prevalence is structurally blind to leaves: an application
has no reverse-dependents no matter how widely it is installed. Download counts
and repository statistics would cover the same gap but are not published
uniformly across registries.

Packages registered after the model's knowledge horizon are floored to zero and
ride the prevalence term instead, since the model cannot know an artifact
published after its training data. Re-measure the horizon per model generation.

Scoring is cached by model, prompt, and package, so reruns only pay for new
packages.`,
		Args: cobra.NoArgs,
		RunE: cli.RunE(&cfg, cli.SkipArgs[prominenceConfig], InitDeps, prominenceHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project for Vertex AI")
	set.StringVar(&cfg.Location, "location", "global", "Vertex AI location")
	set.StringVar(&cfg.Model, "model", llm.GeminiFlash, "Gemini model id served via Vertex")
	set.StringVar(&cfg.Corpus, "corpus", "", "JSONL of {ecosystem, package, [registered]} to score; a prevalence export works")
	set.StringVar(&cfg.File, "file", "", "re-rank an already-scored JSONL export instead of calling the model")
	set.StringVar(&cfg.Out, "out", "", "write the ranked JSONL export to this path or gs:// URI")
	set.StringVar(&cfg.Cache, "cache", "", "JSON score cache keyed by model, prompt, ecosystem, and package")
	set.StringVar(&cfg.Horizon, "horizon", "", "knowledge-horizon date YYYY-MM-DD; packages registered after it are floored")
	set.IntVar(&cfg.Batch, "batch", 15, "packages per model call")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
