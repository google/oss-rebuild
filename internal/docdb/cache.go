// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// cacheSlack starts segment replay this far before the base watermark.
// An update stamped X may not be durably written until X+slack, so a
// base built in between misses it and only a segment named before the
// watermark carries it.
const cacheSlack = 2 * time.Minute

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

	mu         sync.Mutex
	db         *sqlite3.Conn
	dbPath     string
	watermark  time.Time
	baseTag    string
	refusedTag string // base tag already downloaded and refused on schema
	// TODO: Persist replay progress in the copy itself (an applied
	// watermark) if the hydrated database ever needs reuse across processes.
	applied string // newest applied segment name
}

// OpenCache hydrates a cache of up's base object and refreshes it in the
// background every interval until Close. A base from any era but
// up.Schema is refused.
func OpenCache(ctx context.Context, up Upstream, interval time.Duration) (*Cache, error) {
	dir, err := os.MkdirTemp("", "docdb-cache-")
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
		return errors.New("cache is closed")
	}
	return f(c.db)
}

// Freshness returns the time through which the served data is complete.
// This is the base watermark and is advanced by applied delta segments.
func (c *Cache) Freshness() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applied != "" {
		if t, err := SegmentTime(c.applied); err == nil {
			return t
		}
	}
	return c.watermark
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

// sync converges the local copy on the upstream.
// It rehydrates when the base object has been replaced and otherwise applies
// new delta segments in place.
func (c *Cache) sync() error {
	info, err := c.up.FS.Stat(c.up.Object)
	if err != nil {
		return errors.Wrap(err, "checking base object")
	}
	// mtime plus size fingerprints the base's content. A same-length
	// rewrite within the filesystem's timestamp resolution goes unseen.
	tag := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	c.mu.Lock()
	baseTag, refusedTag, after, watermark := c.baseTag, c.refusedTag, c.applied, c.watermark
	c.mu.Unlock()
	if refusedTag != "" && tag == refusedTag {
		// Already downloaded and refused. Skip until it changes again.
		return nil
	}
	if tag != baseTag {
		return c.hydrate(tag)
	}
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
		}
		c.mu.Unlock()
		if err != nil {
			return errors.Wrapf(err, "applying %s", s.name)
		}
	}
	return nil
}

// hydrate downloads the base object at tag, verifies its schema era, replays
// segments past its watermark, and swaps the result in.
func (c *Cache) hydrate(tag string) (err error) {
	f, err := os.CreateTemp(c.dir, "base-*.db")
	if err != nil {
		return errors.Wrap(err, "creating local copy")
	}
	path := f.Name()
	f.Close()
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	if err := sqlitex.Fetch(c.up.FS, c.up.Object, path); err != nil {
		return err
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		return errors.Wrap(err, "opening database")
	}
	if err := sqlitex.CheckVersion(db, c.up.Schema); err != nil {
		db.Close()
		c.mu.Lock()
		c.refusedTag = tag
		c.mu.Unlock()
		return err
	}
	watermark, err := c.up.Watermark(db)
	if err != nil {
		db.Close()
		return errors.Wrap(err, "reading watermark")
	}
	segs, err := fetchNewSegments(c.up.FS, c.up.Deltas, "", watermark)
	if err != nil {
		db.Close()
		return err
	}
	applied := ""
	for _, s := range segs {
		if err := applyRecords(db, c.up.Defs, s.recs); err != nil {
			db.Close()
			return errors.Wrapf(err, "applying %s", s.name)
		}
		applied = s.name
	}
	c.mu.Lock()
	old, oldPath := c.db, c.dbPath
	c.db, c.dbPath, c.watermark, c.baseTag, c.applied, c.refusedTag = db, path, watermark, tag, applied, ""
	c.mu.Unlock()
	if old != nil {
		old.Close()
		os.Remove(oldPath)
	}
	return nil
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
	var segs []segment
	for _, name := range names {
		if name == after {
			// The listing is inclusive of the resume point.
			continue
		}
		recs, err := fetchSegment(src, name)
		if err != nil {
			return nil, errors.Wrapf(err, "fetching %s", name)
		}
		segs = append(segs, segment{name: name, recs: recs})
	}
	return segs, nil
}
