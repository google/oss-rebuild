// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command inferbench times one in-process inference, clone or repo lookup, and
// prints the result as a JSON line. Building it with and without go-git2
// compares the two on the same targets. Registry responses are cached on disk
// so both see the same inputs and no network time. The crates index lookup is
// stubbed, since it does not read the package's repository.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

var (
	impl      = flag.String("impl", "", "label for the implementation under test")
	mode      = flag.String("mode", "infer", "infer, clone, or repo")
	ecosystem = flag.String("ecosystem", "", "")
	pkg       = flag.String("package", "", "")
	version   = flag.String("version", "", "")
	artifact  = flag.String("artifact", "", "")
	cacheDir  = flag.String("http-cache", "", "directory caching registry responses")
)

// cachingClient serves GETs from cacheDir, fetching and storing misses.
type cachingClient struct {
	dir string
}

type cachedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (c *cachingClient) Do(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return http.DefaultClient.Do(req)
	}
	sum := sha256.Sum256([]byte(req.URL.String() + "\x00" + req.Header.Get("Accept")))
	p := filepath.Join(c.dir, hex.EncodeToString(sum[:]))
	var cr cachedResponse
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &cr) == nil {
		return cr.response(req), nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	cr = cachedResponse{Status: resp.StatusCode, Header: resp.Header, Body: body}
	if b, err := json.Marshal(cr); err == nil {
		os.WriteFile(p, b, 0o644)
	}
	return cr.response(req), nil
}

func (cr cachedResponse) response(req *http.Request) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", cr.Status, http.StatusText(cr.Status)),
		StatusCode:    cr.Status,
		Header:        cr.Header,
		Body:          io.NopCloser(bytes.NewReader(cr.Body)),
		ContentLength: int64(len(cr.Body)),
		Request:       req,
	}
}

type result struct {
	Impl     string                `json:"impl"`
	Mode     string                `json:"mode"`
	Target   string                `json:"target"`
	Repo     string                `json:"repo,omitempty"`
	Seconds  float64               `json:"seconds"`
	Error    string                `json:"error,omitempty"`
	Strategy *schema.StrategyOneOf `json:"strategy,omitempty"`
}

func main() {
	flag.Parse()
	ctx := context.Background()
	if err := os.MkdirAll(*cacheDir, 0o755); err != nil {
		log.Fatal(err)
	}
	client := &cachingClient{dir: *cacheDir}
	t := rebuild.Target{Ecosystem: rebuild.Ecosystem(*ecosystem), Package: *pkg, Version: *version, Artifact: *artifact}
	res := result{Impl: *impl, Mode: *mode, Target: fmt.Sprintf("%s:%s@%s", t.Ecosystem, t.Package, t.Version)}
	start := time.Now()
	var err error
	switch *mode {
	case "repo":
		ctx := context.WithValue(ctx, rebuild.HTTPBasicClientID, client)
		res.Repo, err = meta.AllRebuilders[t.Ecosystem].InferRepo(ctx, t, meta.NewRegistryMux(client))
	case "clone":
		var d time.Duration
		d, res.Repo, err = cloneOnly(ctx, client, t)
		start = time.Now().Add(-d)
	case "infer":
		deps := &inferenceservice.InferDeps{
			HTTPClient: client,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{Worktree: memfs.New(), Storer: gitx.NewInMemoryStorer()}
			},
			CratesRegistryStub: func(context.Context, cratesregistryservice.FindRegistryCommitRequest) (*cratesregistryservice.FindRegistryCommitResponse, error) {
				return &cratesregistryservice.FindRegistryCommitResponse{CommitHash: strings.Repeat("0", 40)}, nil
			},
		}
		req := schema.InferenceRequest{Ecosystem: t.Ecosystem, Package: t.Package, Version: t.Version, Artifact: t.Artifact}
		res.Strategy, err = inferenceservice.Infer(ctx, req, deps)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	res.Seconds = time.Since(start).Seconds()
	if err != nil {
		res.Error = err.Error()
	}
	b, _ := json.Marshal(res)
	fmt.Println(string(b))
}

// cloneOnly runs the clone inference would, into the same in-memory storage,
// and returns how long the clone took.
func cloneOnly(ctx context.Context, client *cachingClient, t rebuild.Target) (time.Duration, string, error) {
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, client)
	mux := meta.NewRegistryMux(client)
	repo, err := meta.AllRebuilders[t.Ecosystem].InferRepo(ctx, t, mux)
	if err != nil {
		return 0, "", err
	}
	start := time.Now()
	r, err := rebuild.LoadRepo(ctx, t.Package, gitx.NewInMemoryStorer(), memfs.New(), git.CloneOptions{URL: repo, RecurseSubmodules: git.NoRecurseSubmodules, NoCheckout: true})
	d := time.Since(start)
	if err != nil {
		return d, repo, err
	}
	if c, ok := any(r).(io.Closer); ok {
		c.Close()
	}
	return d, repo, nil
}
