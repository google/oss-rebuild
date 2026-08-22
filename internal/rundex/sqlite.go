// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundex

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Querier serializes access to a snapshot database connection. An
// implementation may swap the underlying database between calls (the
// snapshot cache does, on refresh).
type Querier interface {
	Query(func(*sqlite3.Conn) error) error
}

// SingleConn is a Querier over one fixed connection.
type SingleConn struct {
	mu sync.Mutex
	db *sqlite3.Conn
}

func NewSingleConn(db *sqlite3.Conn) *SingleConn { return &SingleConn{db: db} }

func (c *SingleConn) Query(f func(*sqlite3.Conn) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return f(c.db)
}

// SQLite serves rundex reads from a snapshot database.
type SQLite struct {
	q Querier
}

var _ Reader = &SQLite{}
var _ SessionReader = &SQLite{}

// NewSQLite creates a Reader/SessionReader over a snapshot database.
func NewSQLite(q Querier) *SQLite { return &SQLite{q: q} }

// cond accumulates a WHERE clause and its bind arguments.
type cond struct {
	exprs []string
	args  []string
}

func (c *cond) add(expr string, arg string) {
	c.exprs = append(c.exprs, expr)
	c.args = append(c.args, arg)
}

func (c *cond) in(col string, vals []string) {
	c.exprs = append(c.exprs, col+" IN (?"+strings.Repeat(", ?", len(vals)-1)+")")
	c.args = append(c.args, vals...)
}

func (c *cond) clause() string {
	if len(c.exprs) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c.exprs, " AND ")
}

// queryRaw runs a SELECT whose single result column is a raw source document
// and decodes each row into T.
func queryRaw[T any](q Querier, sql string, args []string) ([]T, error) {
	var out []T
	err := q.Query(func(db *sqlite3.Conn) error {
		stmt, _, err := db.Prepare(sql)
		if err != nil {
			return errors.Wrap(err, "preparing query")
		}
		defer stmt.Close()
		for i, a := range args {
			if err := stmt.BindText(i+1, a); err != nil {
				return errors.Wrap(err, "binding argument")
			}
		}
		for stmt.Step() {
			var v T
			if err := json.Unmarshal([]byte(stmt.ColumnText(0)), &v); err != nil {
				return errors.Wrap(err, "decoding raw document")
			}
			out = append(out, v)
		}
		return errors.Wrap(stmt.Err(), "executing query")
	})
	return out, err
}

func rebuildChan(attempts []schema.RebuildAttempt) <-chan Rebuild {
	ch := make(chan Rebuild, len(attempts))
	for _, a := range attempts {
		ch <- Rebuild{RebuildAttempt: a}
	}
	close(ch)
	return ch
}

func targetCond(c *cond, t rebuild.Target) {
	if t.Ecosystem != "" {
		c.add("ecosystem = ?", string(t.Ecosystem))
	}
	if t.Package != "" {
		c.add("package = ?", t.Package)
	}
	if t.Version != "" {
		c.add("version = ?", t.Version)
	}
	if t.Artifact != "" {
		c.add("artifact = ?", t.Artifact)
	}
}

// FetchRebuilds fetches Rebuild objects out of the snapshot database.
func (s *SQLite) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) ([]Rebuild, error) {
	if len(req.Executors) != 0 && len(req.Runs) != 0 {
		return nil, errors.New("only provide one of executors and runs")
	}
	if req.Bench != nil && req.Bench.Count == 0 {
		return nil, errors.New("empty bench provided")
	}
	var c cond
	if req.Target != nil {
		targetCond(&c, *req.Target)
	}
	if len(req.Executors) != 0 {
		c.in("executor_version", req.Executors)
	}
	if len(req.Runs) != 0 {
		c.in("run_id", req.Runs)
	}
	sql := "SELECT raw FROM attempts" + c.clause() + " ORDER BY created DESC"
	// The bench/tracked filters run client-side in filterRebuilds, so a
	// pre-filter LIMIT would drop rows they keep. The pending, prefix, and
	// pattern filters are also client-side, but LIMIT applies over them
	// anyway to match the Firestore reader, which limits server-side before
	// those same filters.
	if req.Limit > 0 && req.Bench == nil && req.Tracked == nil {
		sql += " LIMIT " + strconv.Itoa(req.Limit)
	}
	attempts, err := queryRaw[schema.RebuildAttempt](s.q, sql, c.args)
	if err != nil {
		return nil, err
	}
	return filterRebuilds(rebuildChan(attempts), req), nil
}

func (s *SQLite) recent(c cond) ([]Rebuild, error) {
	sql := "SELECT raw FROM attempts" + c.clause() + " ORDER BY created DESC LIMIT 100"
	attempts, err := queryRaw[schema.RebuildAttempt](s.q, sql, c.args)
	if err != nil {
		return nil, err
	}
	// Match the Firestore reader: filter pending attempts and clean Message.
	return filterRebuilds(rebuildChan(attempts), &FetchRebuildRequest{}), nil
}

// RecentRebuilds fetches the 100 most recent rebuild results.
func (s *SQLite) RecentRebuilds(ctx context.Context) ([]Rebuild, error) {
	return s.recent(cond{})
}

// RecentEcosystemRebuilds fetches the 100 most recent rebuild results for a specific ecosystem.
func (s *SQLite) RecentEcosystemRebuilds(ctx context.Context, eco rebuild.Ecosystem) ([]Rebuild, error) {
	var c cond
	c.add("ecosystem = ?", string(eco))
	return s.recent(c)
}

// RecentPackageRebuilds fetches the 100 most recent rebuild results for a specific package.
func (s *SQLite) RecentPackageRebuilds(ctx context.Context, eco rebuild.Ecosystem, pkg string) ([]Rebuild, error) {
	var c cond
	c.add("ecosystem = ?", string(eco))
	c.add("package = ?", pkg)
	return s.recent(c)
}

// FetchAttempt fetches a specific Rebuild object.
func (s *SQLite) FetchAttempt(ctx context.Context, target rebuild.Target, runID string) (Rebuild, error) {
	var c cond
	targetCond(&c, target)
	c.add("run_id = ?", runID)
	// A partial target can match several attempts in one run. Take the newest.
	attempts, err := queryRaw[schema.RebuildAttempt](s.q, "SELECT raw FROM attempts"+c.clause()+" ORDER BY created DESC LIMIT 1", c.args)
	if err != nil {
		return Rebuild{}, err
	}
	if len(attempts) == 0 {
		return Rebuild{}, errors.Errorf("attempt not found: %s %s", target.Package, runID)
	}
	return Rebuild{RebuildAttempt: attempts[0]}, nil
}

// LatestTrackedPackages fetches the most recent completed rebuild result for
// each tracked package.
func (s *SQLite) LatestTrackedPackages(ctx context.Context, tracked feed.TrackedPackageIndex) ([]Rebuild, error) {
	sql := `SELECT raw FROM (
		SELECT raw, ecosystem, package,
			ROW_NUMBER() OVER (PARTITION BY ecosystem, package ORDER BY created DESC) rn
		FROM attempts WHERE status != ?
	) WHERE rn = 1`
	attempts, err := queryRaw[schema.RebuildAttempt](s.q, sql, []string{string(schema.RebuildStatusRunning)})
	if err != nil {
		return nil, err
	}
	var res []Rebuild
	for _, a := range attempts {
		if pkgs, ok := tracked[rebuild.Ecosystem(a.Ecosystem)]; ok && pkgs[a.Package] {
			res = append(res, Rebuild{RebuildAttempt: a})
		}
	}
	return res, nil
}

// FetchRuns fetches Runs out of the snapshot database.
func (s *SQLite) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	var c cond
	if len(opts.IDs) != 0 {
		c.in("run_id", opts.IDs)
	}
	if opts.BenchmarkHash != "" {
		c.add("benchmark_hash = ?", opts.BenchmarkHash)
	}
	if opts.BenchmarkName != "" {
		c.add("benchmark_name = ?", opts.BenchmarkName)
	}
	runs, err := queryRaw[schema.Run](s.q, "SELECT raw FROM runs"+c.clause()+" ORDER BY run_id", c.args)
	if err != nil {
		return nil, err
	}
	var res []Run
	for _, r := range runs {
		res = append(res, FromRun(r))
	}
	return res, nil
}

func (s *SQLite) FetchSessions(ctx context.Context, req *FetchSessionsReq) ([]schema.AgentSession, error) {
	var c cond
	if len(req.IDs) != 0 {
		c.in("session_id", req.IDs)
	}
	if req.StopReason != "" {
		c.add("stop_reason = ?", req.StopReason)
	}
	if !req.Since.IsZero() {
		c.add("created >= ?", sqlitex.TimeColumn(req.Since))
	}
	if !req.Until.IsZero() {
		c.add("created <= ?", sqlitex.TimeColumn(req.Until))
	}
	if req.PartialTarget != nil {
		targetCond(&c, *req.PartialTarget)
	}
	sql := "SELECT raw FROM agent_sessions" + c.clause()
	if req.Limit > 0 {
		sql += " ORDER BY created DESC LIMIT " + strconv.Itoa(req.Limit)
	}
	sessions, err := queryRaw[schema.AgentSession](s.q, sql, c.args)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(sessions, func(a, b schema.AgentSession) int {
		return a.Created.Compare(b.Created)
	})
	return sessions, nil
}

func (s *SQLite) FetchIterations(ctx context.Context, req *FetchIterationsReq) ([]schema.AgentIteration, error) {
	if req.SessionID == "" {
		return nil, errors.New("empty session ID provided")
	}
	var c cond
	c.add("session_id = ?", req.SessionID)
	if len(req.IterationIDs) != 0 {
		c.in("iteration_id", req.IterationIDs)
	}
	iterations, err := queryRaw[schema.AgentIteration](s.q, "SELECT raw FROM agent_iterations"+c.clause(), c.args)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(iterations, func(a, b schema.AgentIteration) int {
		return a.Created.Compare(b.Created)
	})
	return iterations, nil
}
