// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// cacheSlack starts segment replay this far before the base watermark.
// An update stamped X may not be durably written until X+slack, so a
// base built in between misses it and only a segment named before the
// watermark carries it.
const cacheSlack = 2 * time.Minute

// fetchParallelism bounds concurrent segment reads during a sync.
const fetchParallelism = 16

// Upstream names the published database a Cache follows: the filesystem
// holding it, its gzip base object, the prefix its delta segments land
// under, the doc tables those segments replay into, and the schema era
// this binary reads. Watermark reads the time through which the base is
// complete, which is where segment replay resumes.
type Upstream struct {
	FS        billy.Filesystem
	Object    string
	Deltas    string
	Defs      []TableDef
	Schema    int
	Watermark func(*sqlite3.Conn) (time.Time, error)
}

// Cache serves an updated local copy of an upstream database. Contents are
// refreshed at the specified interval, applying new delta segments
// incrementally and rehydrating wholesale when a newer full base becomes
// available. Query serializes access to the current connection.
type Cache struct {
	up       Upstream
	interval time.Duration
	dir      string
	stop     context.CancelFunc
	done     chan struct{}
	fresh    atomic.Pointer[time.Time] // Freshness, read without mu

	mu         sync.Mutex
	db         *sqlite3.Conn
	watermark  time.Time
	baseTag    string
	refusedTag string // base tag already downloaded and refused on schema
	// TODO: Persist replay progress in the copy itself (an applied
	// watermark) if the hydrated database ever needs reuse across processes.
	applied string // newest applied segment name
}

// cacheDirPrefix and the owner's pid start each cache directory's name, so a
// later cache can tell a dead owner's copy from a live one.
const cacheDirPrefix = "docdb-cache-"

// OpenCache hydrates a cache of up's base object and refreshes it in the
// background every interval until Close. A base from any era but
// up.Schema is refused.
func OpenCache(ctx context.Context, up Upstream, interval time.Duration) (*Cache, error) {
	sweepOrphans()
	dir, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", cacheDirPrefix, os.Getpid()))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache directory")
	}
	c := &Cache{up: up, interval: interval, dir: dir, done: make(chan struct{})}
	if err := c.sync(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = cancel
	go c.refreshLoop(bg)
	return c, nil
}

// Query runs f with the current database, serialized against refreshes.
func (c *Cache) Query(f func(*sqlite3.Conn) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return errors.New("cache has no local copy")
	}
	return f(c.db)
}

// Freshness returns the time through which the served data is complete.
// This is the base watermark and is advanced by applied delta segments.
// It does not wait on queries or refreshes. Through a rehydration, and
// after one fails, it reports the last copy opened.
func (c *Cache) Freshness() time.Time {
	if t := c.fresh.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// publishFreshness sets what Freshness reports to the current copy's
// watermark, advanced by its newest applied segment. mu must be held.
func (c *Cache) publishFreshness() {
	t := c.watermark
	if c.applied != "" {
		if st, err := SegmentTime(c.applied); err == nil {
			t = st
		}
	}
	c.fresh.Store(&t)
}

// Close stops refreshing, flushes in-flight refresh, and removes local data.
func (c *Cache) Close() error {
	if c.stop != nil {
		c.stop()
		<-c.done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil {
		c.db.Close()
		c.db = nil
	}
	return os.RemoveAll(c.dir)
}

// sweepOrphans removes the cache directories of processes that died without
// Close. Each holds a full copy until the temp directory is cleared.
func sweepOrphans() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), cacheDirPrefix+"*"))
	for _, dir := range dirs {
		var pid int
		if _, err := fmt.Sscanf(filepath.Base(dir), cacheDirPrefix+"%d-", &pid); err != nil || alive(pid) {
			continue
		}
		os.RemoveAll(dir)
	}
}

// alive reports whether a process with pid may exist. Only a confirmed exit
// reads as dead. A reused pid reads as alive, which delays its sweep.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return !errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

func (c *Cache) refreshLoop(ctx context.Context) {
	defer close(c.done)
	tick := time.NewTicker(c.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// NOTE: A failed refresh keeps serving the current, stale copy.
			// The next tick retries.
			if err := c.sync(); err != nil {
				log.Printf("docdb cache refresh: %v", err)
			}
		}
	}
}

// sync converges the local copy on the upstream. It rehydrates when the
// base object has been replaced, then applies new delta segments in place.
func (c *Cache) sync() error {
	info, err := c.up.FS.Stat(c.up.Object)
	if err != nil {
		return errors.Wrap(err, "checking base object")
	}
	// mtime plus size fingerprints the base's content. A same-length
	// rewrite within the filesystem's timestamp resolution goes unseen.
	tag := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	c.mu.Lock()
	baseTag, refusedTag := c.baseTag, c.refusedTag
	c.mu.Unlock()
	if refusedTag != "" && tag == refusedTag {
		// Already downloaded and refused. Skip until it changes again.
		return nil
	}
	if tag != baseTag {
		if err := c.hydrate(tag); err != nil {
			return err
		}
	}
	c.mu.Lock()
	after, watermark := c.applied, c.watermark
	c.mu.Unlock()
	// NOTE: Fetch before taking query lock.
	segs, err := fetchNewSegments(c.up.FS, c.up.Deltas, after, watermark)
	if err != nil {
		return err
	}
	for _, s := range segs {
		c.mu.Lock()
		err := applyRecords(c.db, c.up.Defs, s.recs)
		if err == nil {
			c.applied = s.name
			c.publishFreshness()
		}
		c.mu.Unlock()
		if err != nil {
			return errors.Wrapf(err, "applying %s", s.name)
		}
	}
	return nil
}

// hydrate replaces the local copy with the base at tag. It downloads and
// era-checks the compressed base while the old copy serves, then drops the
// old copy before unpacking, so two never coexist. Queries wait out the swap.
func (c *Cache) hydrate(tag string) error {
	staged := filepath.Join(c.dir, "base.db.gz")
	defer os.Remove(staged)
	if err := download(c.up.FS, c.up.Object, staged); err != nil {
		return err
	}
	f, err := os.Open(staged)
	if err != nil {
		return err
	}
	err = sqlitex.CheckPublishedVersion(f, c.up.Schema)
	f.Close()
	if err != nil {
		c.mu.Lock()
		c.refusedTag = tag
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil {
		c.db.Close()
	}
	// Until the new copy opens, there is nothing to serve. The cleared tag
	// makes the next sync retry.
	c.db, c.watermark, c.baseTag, c.applied = nil, time.Time{}, "", ""
	path := filepath.Join(c.dir, "base.db")
	db, watermark, err := c.unpack(staged, path)
	if err != nil {
		os.Remove(path)
		return err
	}
	c.db, c.watermark, c.baseTag, c.refusedTag = db, watermark, tag, ""
	c.publishFreshness()
	return nil
}

func (c *Cache) unpack(staged, path string) (*sqlite3.Conn, time.Time, error) {
	f, err := os.Open(staged)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	if err := sqlitex.Unpack(f, path); err != nil {
		return nil, time.Time{}, err
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, time.Time{}, errors.Wrap(err, "opening database")
	}
	// The copy is a temp file rebuilt from the base on any fault, so skip
	// fsync, which would dominate segment replay.
	if err := db.Exec("PRAGMA synchronous=OFF; PRAGMA journal_mode=MEMORY"); err != nil {
		db.Close()
		return nil, time.Time{}, errors.Wrap(err, "configuring local copy")
	}
	watermark, err := c.up.Watermark(db)
	if err != nil {
		db.Close()
		return nil, time.Time{}, errors.Wrap(err, "reading watermark")
	}
	return db, watermark, nil
}

// download copies the object fs holds under name, as stored, to path.
func download(fs billy.Basic, name, path string) error {
	r, err := fs.Open(name)
	if err != nil {
		return errors.Wrapf(err, "opening %s", name)
	}
	defer r.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return errors.Wrapf(err, "downloading %s", name)
	}
	return f.Close()
}

// segment is one fetched delta segment, parsed and ready to apply.
type segment struct {
	name string
	recs []deltaRecord
}

// fetchNewSegments lists and reads every segment newer than after (a
// segment name) or, when after is empty, every segment from watermark
// minus the slack window on, in write order.
func fetchNewSegments(src billy.Filesystem, prefix, after string, watermark time.Time) ([]segment, error) {
	start := after
	if start == "" {
		start = SegmentName(prefix, watermark.Add(-cacheSlack))
	}
	names, err := ListSegments(src, prefix, start)
	if err != nil {
		return nil, errors.Wrap(err, "listing segments")
	}
	if len(names) > 0 && names[0] == after {
		// The listing is inclusive of the resume point.
		names = names[1:]
	}
	// A segment costs one read's latency, not its size, so fetch in parallel
	// and apply in order.
	segs := make([]segment, len(names))
	g := new(errgroup.Group)
	g.SetLimit(fetchParallelism)
	for i, name := range names {
		g.Go(func() error {
			recs, err := fetchSegment(src, name)
			if err != nil {
				return errors.Wrapf(err, "fetching %s", name)
			}
			segs[i] = segment{name: name, recs: recs}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return segs, nil
}
