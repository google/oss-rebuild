# crates.io registry index

Manages crates.io registry git repositories to enable reproducible dependency
resolution by providing access to both current and historical registry states.

## Overview

This package provides two main capabilities:

1. **Index Storage (IndexManager)**: Manages the lifecycle of multiple git
   repositories (current + snapshots) with concurrent access
2. **Index Search (FindRegistryResolution)**: Finds the earliest registry commit
   where a set of dependencies were available

## Index Storage

### Purpose

The `IndexManager` coordinates access to multiple registry repositories,
handling fetch/update operations, LRU eviction, and concurrent access control.

### Repository Types

**Current Index**

- Mutable, frequently updated repository
- Source: `https://github.com/rust-lang/crates.io-index.git`
- Automatically refreshed based on `CurrentUpdateInterval`
- Contains the latest state of all published crates
- Stored in `{filesystem}/current/`

**Snapshot Indices**

- Immutable historical snapshots (never updated after fetching)
- Source: `https://github.com/rust-lang/crates.io-index-archive.git`
- Identified by date (YYYY-MM-DD format, e.g., "2024-01-15")
- Subject to LRU eviction when `MaxSnapshots` limit is reached
- Stored in `{filesystem}/snapshot-{date}/`

### Concurrency Design

**Invariants:**

- All state mutations (fetch, update, eviction) go through `acquireLoop`
  coordinator goroutine
- Repository handles hold read locks (`RLock`) preventing eviction while in use
- `GetRepositories()` acquires all locks atomically (no partial failures)
- Eviction does not block on the LRU snapshot, using `TryLock` with backoff to find best candidate

**What's serialized:**

- Repository fetch/clone operations
- Update operations on current index
- LRU eviction decisions
- All state changes to `managedRepository` structs

**What's concurrent:**

- Multiple `GetRepositories()` calls queue to coordinator
- Parallel git operations within a single multi-repo fetch
- Reading from acquired repository handles (non-exclusive)
- Multiple goroutines holding handles simultaneously

### Repository Lifecycle

**State transitions:**

```
NOT_EXISTS → FETCHING → READY ⟷ UPDATING (current index only)
                ↓
              READY → (evicted) → NOT_EXISTS (snapshots only)
```

**Lifecycle stages:**

1. **On-Demand Fetching**: Repositories are cloned lazily on first request
2. **Automatic Updates**: Current index refreshes after `CurrentUpdateInterval` elapses
3. **Handle Management**: Callers receive a `RepositoryHandle` that must be `Close()`d
4. **LRU Eviction**: When snapshot limit exceeded, least-recently-used snapshots removed
5. **Graceful Shutdown**: `IndexManager.Close()` stops background operations and releases resources

### Usage

See [`FindRegistryCommit`](../../../internal/api/cratesregistryservice/findcommit.go) for complete
production usage pattern.

**Minimal setup:**

```go
import (
    "github.com/go-git/go-billy/v5/osfs"
    "github.com/google/oss-rebuild/pkg/registry/cratesio/index"
)

cfg := index.IndexManagerConfig{
    Filesystem:            osfs.New("/var/cache/registry"),
    MaxSnapshots:          4,
    CurrentUpdateInterval: 30 * time.Minute,
}
manager, err := index.NewIndexManagerFromFS(cfg)
if err != nil {
    return err
}
defer manager.Close()
```

**Repository acquisition pattern:**

```go
// Build repository keys
keys := []index.RepositoryKey{
    {Type: index.CurrentIndex},
    {Type: index.SnapshotIndex, Name: "2024-01-15"},
}

// Fetch with time constraint
opts := &index.RepoOpt{Contains: &publishTime}
handles, err := manager.GetRepositories(ctx, keys, opts)
if err != nil {
    // Handle error (see Error Handling below)
}
defer func() {
    for _, h := range handles {
        h.Close()
    }
}()

// Use repositories
for _, h := range handles {
    _ = h.Repository // *git.Repository
}
```

**Snapshot discovery:**

```go
// List available snapshots from archive
snapshots, err := index.ListAvailableSnapshots(ctx)
// Returns: ["2023-12-01", "2024-01-01", "2024-02-01", ...]

// See tools/registryscan/main.go:105-138 for filtering logic
```

### Error Handling

**RegistryOutOfDateError**

Returned when `RepoOpt.Contains` time constraint fails (current index HEAD
commit time is before the required time).

Fields:

- `RequiredTime`: The time that was required
- `ActualTime`: The HEAD commit time of current index
- `NextUpdateTime`: When the next automatic update is scheduled
- `UpdateInterval`: The configured update interval

Handling strategy (see [`FindRegistryCommit`](../../../internal/api/cratesregistryservice/findcommit.go)):

```go
if outOfDateErr, ok := err.(*index.RegistryOutOfDateError); ok {
    retryAfter := time.Until(outOfDateErr.NextUpdateTime)
    if retryAfter <= 0 {
        retryAfter = outOfDateErr.UpdateInterval
    }
    // Return retry-after to client or wait and retry
}
```

### Configuration

**IndexManagerConfig** (see `manager.go:107-113`):

| Field                   | Type               | Required | Description                                    |
| ----------------------- | ------------------ | -------- | ---------------------------------------------- |
| `Filesystem`            | `billy.Filesystem` | **Yes**  | Filesystem to store cached repositories        |
| `MaxSnapshots`          | `int`              | **Yes**  | Maximum snapshot repos to cache (LRU eviction) |
| `CurrentUpdateInterval` | `time.Duration`    | **Yes**  | How often to update the current index          |
| `CurrentCloneFunc`      | `gitx.CloneFunc`   | No       | Custom clone function for current index        |
| `SnapshotCloneFunc`     | `gitx.CloneFunc`   | No       | Custom clone function for snapshots            |

**Recommended values:**

- `MaxSnapshots`: 2-4 for typical workloads (balance disk space vs query range)
- `CurrentUpdateInterval`: 15-60 minutes (balance freshness vs update cost)

**Git cache integration** (see `cmd/registry/main.go:72-88`):

Use git-cache for snapshots (immutable) but not current index (changes
frequently). Set `SnapshotCloneFunc` to `cache.Clone` with infinite freshness.

## Performance Considerations

- **First access**: Clones repository (slow, ~seconds to minutes depending on network)
- **Subsequent accesses**: Instant (already cached on disk)
- **Updates**: Current index updates in background (non-blocking)
- **Disk usage**: ~400MB per repository × (1 current + N snapshots)
- **Memory**: Minimal (repositories accessed via git library, not loaded into RAM)

---

## Index Search

### Purpose

`FindRegistryResolution` searches registry history to find the earliest commit
where all specified packages@versions are available. This is useful for
reproducing historical builds and understanding dependency timelines.

### Algorithm

**Two-phase search:**

**Phase 1: Coarse Day-Level Scan**

- Start from the publish date
- Check one commit per ~24-hour window going backwards
- Stop when finding a day with fewer matching packages
- Result: Narrow time window (~1 day) containing the transition point

**Phase 2: Fine Commit-Level Search**

- Within the identified window, examine every commit
- Find the exact first commit where all packages are present
- Result: Specific commit hash + commit time

**Optimization:** Blob hash caching avoids re-reading unchanged files across
commits.

### Multi-Repository Strategy

Since registry histories may cross snapshot boundaries, we need to handle the
possibility of multiple index repositories:

**Input:** Ordered repositories (newest → oldest)

**Process:** Search each repository sequentially until finding a boundary within
a repository.

**Edge cases at repository boundaries:**

- If previous repo has no boundary AND current repo has fewer matches → boundary is at repo transition
- If subsequent repo has zero matches → skip (expected at boundaries)

See `find.go:32-76` for complete implementation.

### Registry Path Mapping

Package names are mapped to file paths using `EntryPath()`:

```
Length  | Format               | Example
--------|----------------------|------------------
1 char  | 1/{name}             | "a" → "1/a"
2 char  | 2/{name}             | "ab" → "2/ab"
3 char  | 3/{c0}/{name}        | "abc" → "3/a/abc"
4+ char | {c0:2}/{c2:4}/{name} | "serde" → "se/rd/serde"
```

Names are lowercased before mapping.

### Usage

See [`FindRegistryResolution`](../../../internal/api/cratesregistryservice/findcommit.go) for a complete example.

**Basic pattern:**

```go
// After acquiring repository handles (see Part 1)
var repos []*git.Repository
for _, h := range handles {
    repos = append(repos, h.Repository)
}

// Parse Cargo.lock to get packages
packages, err := cargolock.Parse(lockfileContent)

// Find resolution
resolution, err := index.FindRegistryResolution(repos, packages, publishTime, nil)
if err != nil {
    return err
}

fmt.Printf("Commit: %s at %s\n",
    resolution.CommitHash,
    resolution.CommitTime.Format(time.RFC3339))
```

**Verbose logging:**

Pass `&index.FindConfig{VerboseLogging: true}` as the final argument to see
detailed commit analysis progress.
