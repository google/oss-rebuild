// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/ncruces/go-sqlite3"
)

// Column indices for the rebuilds table.
const (
	rebuildsColEcosystem       = 0
	rebuildsColPackage         = 1
	rebuildsColVersion         = 2
	rebuildsColArtifact        = 3
	rebuildsColSuccess         = 4
	rebuildsColMessage         = 5
	rebuildsColExecutorVersion = 6
	rebuildsColRunID           = 7
	rebuildsColBuildID         = 8
	rebuildsColStarted         = 9
	rebuildsColEnded           = 10
	rebuildsColDurationS       = 11
)

// BestIndex bitmask flags for pushdown constraints.
const (
	rebuildsHasRunID = 1 << iota
)

type rebuildsTable struct {
	reader rundex.Reader
	ctx    context.Context
	runIDs []string
	bench  *benchmark.PackageSet
}

type rebuildsCursor struct {
	table *rebuildsTable
	rows  []rundex.Rebuild
	pos   int
}

func registerRebuildsVTab(db *sqlite3.Conn, ctx context.Context, reader rundex.Reader, runIDs []string, bench *benchmark.PackageSet) error {
	return sqlite3.CreateModule(db, "rebuilds", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*rebuildsTable, error) {
			err := db.DeclareVTab(`CREATE TABLE rebuilds (
				ecosystem        TEXT,
				package          TEXT,
				version          TEXT,
				artifact         TEXT,
				success          INTEGER,
				message          TEXT,
				executor_version TEXT,
				run_id           TEXT,
				build_id         TEXT,
				started          TEXT,
				ended            TEXT,
				duration_s       REAL
			)`)
			if err != nil {
				return nil, err
			}
			return &rebuildsTable{
				reader: reader,
				ctx:    ctx,
				runIDs: runIDs,
				bench:  bench,
			}, nil
		})
}

func (t *rebuildsTable) BestIndex(idx *sqlite3.IndexInfo) error {
	var idxNum int
	argIdx := 1
	for i, c := range idx.Constraint {
		if !c.Usable {
			continue
		}
		if c.Column == rebuildsColRunID && c.Op == sqlite3.INDEX_CONSTRAINT_EQ {
			idx.ConstraintUsage[i].ArgvIndex = argIdx
			idx.ConstraintUsage[i].Omit = true
			idxNum |= rebuildsHasRunID
			argIdx++
		}
	}
	idx.IdxNum = idxNum
	if idxNum&rebuildsHasRunID != 0 {
		idx.EstimatedCost = 100
		idx.EstimatedRows = 1000
	} else {
		// Without a run_id constraint, use the run IDs from the command flags.
		idx.EstimatedCost = 10000
		idx.EstimatedRows = 100000
	}
	return nil
}

func (t *rebuildsTable) Open() (sqlite3.VTabCursor, error) {
	return &rebuildsCursor{table: t}, nil
}

func (c *rebuildsCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	req := &rundex.FetchRebuildRequest{
		Bench: c.table.bench,
	}
	if idxNum&rebuildsHasRunID != 0 && len(arg) > 0 {
		req.Runs = []string{arg[0].Text()}
	} else {
		req.Runs = c.table.runIDs
	}
	rows, err := c.table.reader.FetchRebuilds(c.table.ctx, req)
	if err != nil {
		return err
	}
	c.rows = rows
	c.pos = 0
	return nil
}

func (c *rebuildsCursor) Next() error {
	c.pos++
	return nil
}

func (c *rebuildsCursor) EOF() bool {
	return c.pos >= len(c.rows)
}

func (c *rebuildsCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case rebuildsColEcosystem:
		ctx.ResultText(r.Ecosystem)
	case rebuildsColPackage:
		ctx.ResultText(r.Package)
	case rebuildsColVersion:
		ctx.ResultText(r.Version)
	case rebuildsColArtifact:
		ctx.ResultText(r.Artifact)
	case rebuildsColSuccess:
		if r.Success {
			ctx.ResultInt64(1)
		} else {
			ctx.ResultInt64(0)
		}
	case rebuildsColMessage:
		ctx.ResultText(r.Message)
	case rebuildsColExecutorVersion:
		ctx.ResultText(r.ExecutorVersion)
	case rebuildsColRunID:
		ctx.ResultText(r.RunID)
	case rebuildsColBuildID:
		ctx.ResultText(r.BuildID)
	case rebuildsColStarted:
		ctx.ResultText(r.Started.Format("2006-01-02T15:04:05Z"))
	case rebuildsColEnded:
		ctx.ResultText(r.Created.Format("2006-01-02T15:04:05Z"))
	case rebuildsColDurationS:
		ctx.ResultFloat(r.Created.Sub(r.Started).Seconds())
	}
	return nil
}

func (c *rebuildsCursor) RowID() (int64, error) {
	return int64(c.pos), nil
}
