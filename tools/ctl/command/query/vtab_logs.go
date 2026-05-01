// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/ncruces/go-sqlite3"
)

// Column indices for the logs table.
const (
	logsColEcosystem = 0
	logsColPackage   = 1
	logsColVersion   = 2
	logsColArtifact  = 3
	logsColRunID     = 4
	logsColContent   = 5
)

// BestIndex bitmask flags for required key columns.
const (
	logsHasEcosystem = 1 << iota
	logsHasPackage
	logsHasVersion
	logsHasArtifact
	logsHasRunID
	logsAllKeys = logsHasEcosystem | logsHasPackage | logsHasVersion | logsHasArtifact | logsHasRunID
)

type logsTable struct {
	assets *assetlocator.MetaAssetStore
	ctx    context.Context
}

type logsCursor struct {
	table   *logsTable
	row     *logsRow // nil if no results
	visited bool
}

type logsRow struct {
	ecosystem string
	pkg       string
	version   string
	artifact  string
	runID     string
	content   string
}

func registerLogsVTab(db *sqlite3.Conn, ctx context.Context, assets *assetlocator.MetaAssetStore) error {
	return sqlite3.CreateModule(db, "logs", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*logsTable, error) {
			err := db.DeclareVTab(`CREATE TABLE logs (
				ecosystem TEXT,
				package   TEXT,
				version   TEXT,
				artifact  TEXT,
				run_id    TEXT,
				content   TEXT
			)`)
			if err != nil {
				return nil, err
			}
			return &logsTable{assets: assets, ctx: ctx}, nil
		})
}

func (t *logsTable) BestIndex(idx *sqlite3.IndexInfo) error {
	var idxNum int
	argIdx := 1
	// IdxStr encodes which column each sequential arg corresponds to.
	// Each byte is the column index. Filter uses this to reconstruct
	// the mapping.
	var colOrder []byte
	for i, c := range idx.Constraint {
		if !c.Usable || c.Op != sqlite3.INDEX_CONSTRAINT_EQ {
			continue
		}
		var bit int
		switch c.Column {
		case logsColEcosystem:
			bit = logsHasEcosystem
		case logsColPackage:
			bit = logsHasPackage
		case logsColVersion:
			bit = logsHasVersion
		case logsColArtifact:
			bit = logsHasArtifact
		case logsColRunID:
			bit = logsHasRunID
		default:
			continue
		}
		if idxNum&bit != 0 {
			continue // already have a constraint for this column
		}
		idx.ConstraintUsage[i].ArgvIndex = argIdx
		idx.ConstraintUsage[i].Omit = true
		idxNum |= bit
		colOrder = append(colOrder, byte(c.Column)+1) // +1 to avoid null bytes in C string
		argIdx++
	}
	idx.IdxNum = idxNum
	idx.IdxStr = string(colOrder)
	if idxNum&logsAllKeys == logsAllKeys {
		idx.EstimatedCost = 1
		idx.EstimatedRows = 1
	} else {
		// Missing key columns: make this prohibitively expensive so SQLite
		// prefers plans that provide all keys (e.g. via a JOIN).
		idx.EstimatedCost = 1e18
		idx.EstimatedRows = 1e15
	}
	return nil
}

func (t *logsTable) Open() (sqlite3.VTabCursor, error) {
	return &logsCursor{table: t}, nil
}

func (c *logsCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	c.row = nil
	c.visited = false
	if idxNum&logsAllKeys != logsAllKeys {
		// Not all key columns provided; return empty.
		return nil
	}
	// Decode args using the column order encoded in idxStr (col indices are +1 to avoid null bytes).
	vals := make(map[int]string, len(idxStr))
	for i, col := range []byte(idxStr) {
		if i < len(arg) {
			vals[int(col)-1] = arg[i].Text()
		}
	}
	target := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(vals[logsColEcosystem]),
		Package:   vals[logsColPackage],
		Version:   vals[logsColVersion],
		Artifact:  vals[logsColArtifact],
	}
	runID := vals[logsColRunID]
	store, err := c.table.assets.For(c.table.ctx, runID)
	if err != nil {
		return err
	}
	row := &logsRow{
		ecosystem: string(target.Ecosystem),
		pkg:       target.Package,
		version:   target.Version,
		artifact:  target.Artifact,
		runID:     runID,
	}
	// Fetch log content.
	r, err := store.Reader(c.table.ctx, rebuild.DebugLogsAsset.For(target))
	if err != nil {
		return err
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	row.content = string(content)
	c.row = row
	return nil
}

func (c *logsCursor) Next() error {
	c.visited = true
	return nil
}

func (c *logsCursor) EOF() bool {
	return c.row == nil || c.visited
}

func (c *logsCursor) Column(ctx sqlite3.Context, col int) error {
	if c.row == nil {
		ctx.ResultNull()
		return nil
	}
	switch col {
	case logsColEcosystem:
		ctx.ResultText(c.row.ecosystem)
	case logsColPackage:
		ctx.ResultText(c.row.pkg)
	case logsColVersion:
		ctx.ResultText(c.row.version)
	case logsColArtifact:
		ctx.ResultText(c.row.artifact)
	case logsColRunID:
		ctx.ResultText(c.row.runID)
	case logsColContent:
		ctx.ResultText(c.row.content)
	}
	return nil
}

func (c *logsCursor) RowID() (int64, error) {
	return 0, nil
}
