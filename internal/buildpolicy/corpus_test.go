// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package buildpolicy

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/llm"
	"google.golang.org/genai"
)

// TestReviewCorpus runs the live reviewer over the corpusRecord JSONL file
// named by BUILDPOLICY_CORPUS and appends verdicts to BUILDPOLICY_CORPUS_OUT,
// skipping records already there. It needs Vertex credentials.
//
//	BUILDPOLICY_CORPUS=eval.jsonl BUILDPOLICY_CORPUS_OUT=out.jsonl \
//	  go test ./internal/buildpolicy -run TestReviewCorpus -timeout 1h
func TestReviewCorpus(t *testing.T) {
	in, out := os.Getenv("BUILDPOLICY_CORPUS"), os.Getenv("BUILDPOLICY_CORPUS_OUT")
	if in == "" || out == "" {
		t.Skip("BUILDPOLICY_CORPUS and BUILDPOLICY_CORPUS_OUT not set")
	}
	project, model := os.Getenv("BUILDPOLICY_PROJECT"), os.Getenv("BUILDPOLICY_MODEL")
	if model == "" {
		model = llm.GeminiFlash
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: project, Location: "global"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	done := map[string]bool{}
	if prev, err := os.Open(out); err == nil {
		sc := bufio.NewScanner(prev)
		sc.Buffer(make([]byte, 1<<20), 1<<26)
		for sc.Scan() {
			var r corpusResult
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				done[r.SID] = true
			}
		}
		prev.Close()
	}
	of, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()
	// Retry transient model failures, as the campaign does.
	retrier := ratex.Retrier{Limiter: ratex.NewBackoffLimiter(time.Second, 2*time.Minute), Attempts: 12, Retryable: llm.IsTransient}
	var mu sync.Mutex
	enc := json.NewEncoder(of)
	sem := make(chan struct{}, 12)
	var wg sync.WaitGroup
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var rec corpusRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if done[rec.SID] {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(rec corpusRecord) {
			defer wg.Done()
			defer func() { <-sem }()
			in := ReviewInput{
				Target:        Target{Package: rec.Pkg, Version: rec.Ver, Ref: rec.Ref},
				Ecosystem:     rec.Eco,
				Artifact:      rec.Artifact,
				Deps:          rec.Deps,
				Build:         rec.Build,
				PreviousError: rec.History,
			}
			res := corpusResult{corpusRecord: rec, Model: model}
			res.History = ""
			var r *Review
			err := retrier.Do(ctx, func() error {
				var err error
				r, _, err = Run(ctx, client, model, in)
				return err
			})
			if err != nil {
				res.Err = err.Error()
			} else {
				res.Review = r
			}
			mu.Lock()
			enc.Encode(res)
			mu.Unlock()
		}(rec)
	}
	wg.Wait()
}

type corpusRecord struct {
	SID      string   `json:"sid"`
	Eco      string   `json:"eco"`
	Pkg      string   `json:"pkg"`
	Ver      string   `json:"ver"`
	Artifact string   `json:"artifact"`
	Ref      string   `json:"ref"`
	Tags     []string `json:"tags"`
	Band     string   `json:"band"`
	Label    string   `json:"label"`
	Deps     string   `json:"deps"`
	Build    string   `json:"build"`
	History  string   `json:"history"` // prior attempt, as the model saw it
}

type corpusResult struct {
	corpusRecord
	Review *Review `json:"review"`
	Model  string  `json:"model"`
	Err    string  `json:"err,omitempty"`
}
