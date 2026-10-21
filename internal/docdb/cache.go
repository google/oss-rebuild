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
	"runtime"
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

// busyTimeout bounds a connection's wait on another's lock, as the writer's
// on readers at a checkpoint.
const busyTimeout = 10 * time.Second

// Upstream names the published database a Cache follows: the filesystem
// holding it, its gzip base object, the prefix its delta segments land
// under, the tables those segments replay into and re-derive, and the
// schema era this binary reads. Watermark reads the time through which the
// base is complete, which is where segment replay resumes. Init, when set,
// prepares every connection the cache opens (collations the derived
// queries need).
type Upstream struct {
	FS        billy.Filesystem
	Object    string
	Deltas    string
	Defs      []TableDef
	Schema    int
	Watermark func(*sqlite3.Conn) (time.Time, error)
	Init      func(*sqlite3.Conn) error
}

// Cache serves an updated local copy of an upstream database. Contents are
// refreshed at the specified interval, applying new delta segments
// incrementally and rehydrating wholesale when a newer full base becomes
// available. Queries run on a pool of read connections while the refresh
// writes through its own in WAL mode, so neither waits on the other. A
// rehydration takes the copy from both.
type Cache struct {
	up       Upstream
	interval time.Duration
	dir      string
	stop     context.CancelFunc
	done     chan struct{}
	fresh    atomic.Pointer[time.Time] // Freshness, read without mu

	mu     sync.RWMutex       // queries hold it shared, a swap of the copy exclusively
	writer *sqlite3.Conn      // the refresh goroutine's alone
	pool   chan *sqlite3.Conn // read connections, each held by one query at a time

	// The refresh goroutine alone reads and writes the fields below.
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

// Query runs f on a read connection, waiting for one to free up and for a
// rehydration in progress.
func (c *Cache) Query(f func(*sqlite3.Conn) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pool == nil {
		return errors.New("cache has no local copy")
	}
	db := <-c.pool
	defer func() { c.pool <- db }()
	return f(db)
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
// watermark, advanced by its newest applied segment.
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
	c.closeCopy()
	return os.RemoveAll(c.dir)
}

// closeCopy closes every connection to the copy. mu must be held
// exclusively, which puts every read connection back in the pool.
func (c *Cache) closeCopy() {
	if c.writer != nil {
		c.writer.Close()
		c.writer = nil
	}
	if c.pool != nil {
		for range cap(c.pool) {
			(<-c.pool).Close()
		}
		c.pool = nil
	}
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
// base object has been replaced, then applies new delta segments in place
// through the writer, which readers do not wait on. Freshness advances once
// the apply has committed.
func (c *Cache) sync() error {
	info, err := c.up.FS.Stat(c.up.Object)
	if err != nil {
		return errors.Wrap(err, "checking base object")
	}
	// mtime plus size fingerprints the base's content. A same-length
	// rewrite within the filesystem's timestamp resolution goes unseen.
	tag := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if tag == c.refusedTag {
		// Already downloaded and refused. Skip until it changes again.
		return nil
	}
	if tag != c.baseTag {
		if err := c.hydrate(tag); err != nil {
			return err
		}
	}
	segs, err := fetchNewSegments(c.up.FS, c.up.Deltas, c.applied, c.watermark)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}
	if err := applySegments(c.writer, c.up.Defs, segs); err != nil {
		return errors.Wrap(err, "applying segments")
	}
	c.applied = segs[len(segs)-1].name
	c.publishFreshness()
	c.checkpoint()
	return nil
}

// checkpoint folds the write-ahead log back into the copy and truncates it,
// so a sync's writes do not accumulate beside the copy. A read still using
// the log holds up the truncate, stalling the refresh but not queries for up
// to busyTimeout. If that runs out, the log stays until the next checkpoint.
func (c *Cache) checkpoint() {
	busy, err := pragmaText(c.writer, "wal_checkpoint(TRUNCATE)")
	if err != nil {
		log.Printf("docdb cache checkpoint: %v", err)
	} else if busy != "0" {
		log.Printf("docdb cache checkpoint: a read held the log past %v", busyTimeout)
	}
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
		c.refusedTag = tag
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCopy()
	// Until the new copy opens, there is nothing to serve. The cleared tag
	// makes the next sync retry.
	c.watermark, c.baseTag, c.applied = time.Time{}, "", ""
	path := filepath.Join(c.dir, "base.db")
	err = unpack(staged, path)
	if err == nil {
		err = c.open(path)
	}
	if err != nil {
		c.closeCopy()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(path + suffix)
		}
		return err
	}
	c.baseTag, c.refusedTag = tag, ""
	c.publishFreshness()
	return nil
}

// open connects to the copy at path: the writer first, in WAL mode, then
// the readers.
func (c *Cache) open(path string) error {
	w, err := c.connect(path)
	if err != nil {
		return err
	}
	c.writer = w
	// The copy is a temp file rebuilt from the base on any fault, so skip
	// fsync, which would dominate segment replay.
	if err := w.Exec("PRAGMA synchronous=OFF"); err != nil {
		return errors.Wrap(err, "configuring writer")
	}
	mode, err := pragmaText(w, "journal_mode=WAL")
	if err != nil {
		return errors.Wrap(err, "configuring writer")
	}
	// NOTE: Environments without working WAL are not supported. gVisor answers
	// wal here and then fails queries with "sqlite3: locking protocol".
	if mode != "wal" {
		return errors.Errorf("copy needs WAL, journal_mode answered %q", mode)
	}
	if c.watermark, err = c.up.Watermark(w); err != nil {
		return errors.Wrap(err, "reading watermark")
	}
	// A connection runs one query at a time and holds its own page cache and
	// sort space, so one per CPU the runtime schedules on keeps queries off
	// each other without a copy that sits idle.
	pool := make(chan *sqlite3.Conn, runtime.GOMAXPROCS(0))
	for range cap(pool) {
		r, err := c.connect(path)
		if err != nil {
			for range len(pool) {
				(<-pool).Close()
			}
			return err
		}
		if err := r.Exec("PRAGMA query_only=1"); err != nil {
			r.Close()
			for range len(pool) {
				(<-pool).Close()
			}
			return errors.Wrap(err, "configuring reader")
		}
		pool <- r
	}
	c.pool = pool
	return nil
}

// connect opens one connection to the copy, prepared by Init.
func (c *Cache) connect(path string) (*sqlite3.Conn, error) {
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "opening database")
	}
	if err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout.Milliseconds())); err != nil {
		db.Close()
		return nil, errors.Wrap(err, "setting busy timeout")
	}
	if c.up.Init != nil {
		if err := c.up.Init(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// pragmaText runs a pragma and returns the text it answers with.
func pragmaText(db *sqlite3.Conn, pragma string) (string, error) {
	stmt, _, err := db.Prepare("PRAGMA " + pragma)
	if err != nil {
		return "", err
	}
	defer stmt.Close()
	if !stmt.Step() {
		return "", stmt.Err()
	}
	return stmt.ColumnText(0), nil
}

// unpack decompresses the staged base into the copy at path.
func unpack(staged, path string) error {
	f, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer f.Close()
	return sqlitex.Unpack(f, path)
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
