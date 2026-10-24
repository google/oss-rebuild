// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"golang.org/x/sync/semaphore"
)

// Memory is the memory an instance's requests reserve before loading their
// repository and artifact. A request waits while those ahead of it hold the
// room it needs, and waiting requests reserve in the order they arrived.
type Memory struct {
	sem      *semaphore.Weighted
	capacity int64
}

// NewMemory returns the Memory of an instance with total bytes, or nil when
// base memory leaves none.
func NewMemory(total int64) *Memory {
	if total <= baseMemory {
		return nil
	}
	return &Memory{sem: semaphore.NewWeighted(total - baseMemory), capacity: total - baseMemory}
}

const (
	exemptThreshold = 256 << 20       // exempt small requests for reservations
	baseMemory      = 4 << 30         // withheld from reservations for the process and exempt requests
	memoryWait      = 5 * time.Minute // longest wait for memory, leaving time to load before the request timeout
)

// reserve holds need bytes until release. A need over the capacity waits for
// it to become available. A wait gives up after memoryWait.
func (m *Memory) reserve(ctx context.Context, need int64) (release func(), err error) {
	if need <= exemptThreshold {
		return func() {}, nil
	}
	need = min(need, m.capacity)
	ctx, cancel := context.WithTimeout(ctx, memoryWait)
	defer cancel()
	if err := m.sem.Acquire(ctx, need); err != nil {
		return func() {}, errors.Wrap(err, "waiting for memory")
	}
	return func() { m.sem.Release(need) }, nil
}

const (
	repoFactor     = 8 // memory per archive byte. approximated based on clone and history walks
	artifactFactor = 3 // memory per artifact byte. approximated based on response, copy, entry unpacking
)

// reserveMemory holds the memory to load repo and t's artifact at the sizes
// the git cache and the registry declare. A cache failure fails the request
// before it waits.
func reserveMemory(ctx context.Context, deps *InferDeps, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux, repo string) (release func(), err error) {
	if deps.Memory == nil {
		return func() {}, nil
	}
	var repoSize int64
	// TODO: Without a git cache the repository goes unreserved. Size it from repo_metrics.
	if c, _ := ctx.Value(rebuild.RepoCacheClientID).(*gitcache.Client); c != nil {
		repoSize, err = c.Size(ctx, repo)
		// CloneRepo reports a repository the cache can't read.
		if err != nil && !errors.Is(err, transport.ErrAuthenticationRequired) {
			return func() {}, errors.Wrap(err, "sizing repository")
		}
	}
	artifactSize, err := artifactBytes(ctx, deps.HTTPClient, rebuilder, t, mux)
	if err != nil {
		// The download may still succeed, so an unsized artifact loads unreserved.
		log.Printf("Artifact size unknown [pkg=%s, version=%v]: %v", t.Package, t.Version, err)
	}
	return deps.Memory.reserve(ctx, repoFactor*repoSize+artifactFactor*artifactSize)
}

// artifactBytes returns the length the registry declares for t's artifact, or
// 0 when it declares none.
func artifactBytes(ctx context.Context, client httpx.BasicClient, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux) (int64, error) {
	u, err := rebuilder.UpstreamURL(ctx, t, mux)
	if err != nil {
		return 0, errors.Wrap(err, "locating artifact")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, errors.Errorf("HEAD %s: %s", u, resp.Status)
	}
	return max(resp.ContentLength, 0), nil
}
