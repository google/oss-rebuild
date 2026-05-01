// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"github.com/ncruces/go-sqlite3"
)

const (
	sgIOColEcosystem      = 0
	sgIOColPackage        = 1
	sgIOColVersion        = 2
	sgIOColArtifact       = 3
	sgIOColRunID          = 4
	sgIOColActionID       = 5
	sgIOColResourceDigest = 6
	sgIOColDirection      = 7
	sgIOColIOType         = 8
	sgIOColTime           = 9
	sgIOColTotalSize      = 10
	sgIOColBytesUsed      = 11
	sgIOColResourceType   = 12
	sgIOColPath           = 13
	sgIOColAddress        = 14
)

const (
	sgIOHasEcosystem = 1 << iota
	sgIOHasPackage
	sgIOHasVersion
	sgIOHasArtifact
	sgIOHasRunID
	sgIOAllKeys = sgIOHasEcosystem | sgIOHasPackage | sgIOHasVersion | sgIOHasArtifact | sgIOHasRunID
)

type sgIOTable struct {
	cache *sgCache
}

type sgIOCursor struct {
	table *sgIOTable
	rows  []sgIORow
	pos   int
}

func registerSGIOVTab(db *sqlite3.Conn, cache *sgCache) error {
	return sqlite3.CreateModule(db, "sg_io", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*sgIOTable, error) {
			err := db.DeclareVTab(`CREATE TABLE sg_io (
				ecosystem       TEXT,
				package         TEXT,
				version         TEXT,
				artifact        TEXT,
				run_id          TEXT,
				action_id       INTEGER,
				resource_digest TEXT,
				direction       TEXT,
				io_type         TEXT,
				time            TEXT,
				total_size      INTEGER,
				bytes_used      INTEGER,
				resource_type   TEXT,
				path            TEXT,
				address         TEXT
			)`)
			if err != nil {
				return nil, err
			}
			return &sgIOTable{cache: cache}, nil
		})
}

const (
	sgIOHasActionID = 1 << 5 // bit after the 5 key bits
)

func (t *sgIOTable) BestIndex(idx *sqlite3.IndexInfo) error {
	var idxNum int
	argIdx := 1
	var colOrder []byte
	colBits := map[int]int{
		sgIOColEcosystem: sgIOHasEcosystem,
		sgIOColPackage:   sgIOHasPackage,
		sgIOColVersion:   sgIOHasVersion,
		sgIOColArtifact:  sgIOHasArtifact,
		sgIOColRunID:     sgIOHasRunID,
		sgIOColActionID:  sgIOHasActionID,
	}
	for i, c := range idx.Constraint {
		if !c.Usable || c.Op != sqlite3.INDEX_CONSTRAINT_EQ {
			continue
		}
		bit, ok := colBits[c.Column]
		if !ok {
			continue
		}
		if idxNum&bit != 0 {
			continue
		}
		idx.ConstraintUsage[i].ArgvIndex = argIdx
		idx.ConstraintUsage[i].Omit = true
		idxNum |= bit
		colOrder = append(colOrder, byte(c.Column)+1)
		argIdx++
	}
	idx.IdxNum = idxNum
	idx.IdxStr = string(colOrder)
	if idxNum&sgIOAllKeys != sgIOAllKeys {
		idx.EstimatedCost = 1e18
		idx.EstimatedRows = 1e15
	} else if idxNum&sgIOHasActionID != 0 {
		// Single action: very cheap (tens of rows).
		idx.EstimatedCost = 1
		idx.EstimatedRows = 100
	} else {
		// All actions: expensive (millions of rows).
		idx.EstimatedCost = 1e6
		idx.EstimatedRows = 1e6
	}
	return nil
}

func (t *sgIOTable) Open() (sqlite3.VTabCursor, error) {
	return &sgIOCursor{table: t}, nil
}

func (c *sgIOCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	c.rows = nil
	c.pos = 0
	if idxNum&sgIOAllKeys != sgIOAllKeys {
		return nil
	}
	vals := decodeIdxStr(idxStr, arg)
	entry, err := c.table.cache.load(
		vals[sgIOColEcosystem], vals[sgIOColPackage],
		vals[sgIOColVersion], vals[sgIOColArtifact], vals[sgIOColRunID])
	if err != nil {
		return err
	}
	if idxNum&sgIOHasActionID != 0 {
		// Single action pushdown: only flatten I/O for this one action.
		actionID := arg[len(arg)-1].Int64() // action_id is last arg
		// Find the correct arg by column mapping.
		for i, col := range []byte(idxStr) {
			if int(col)-1 == sgIOColActionID && i < len(arg) {
				actionID = arg[i].Int64()
				break
			}
		}
		rows, err := entry.ioRowsForAction(c.table.cache.ctx, actionID)
		if err != nil {
			return err
		}
		c.rows = rows
	} else {
		// Full scan: flatten all actions.
		rows, err := entry.allIORows(c.table.cache.ctx)
		if err != nil {
			return err
		}
		c.rows = rows
	}
	return nil
}

func (c *sgIOCursor) Next() error           { c.pos++; return nil }
func (c *sgIOCursor) EOF() bool             { return c.pos >= len(c.rows) }
func (c *sgIOCursor) RowID() (int64, error) { return int64(c.pos), nil }

func (c *sgIOCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case sgIOColEcosystem:
		ctx.ResultText(r.ecosystem)
	case sgIOColPackage:
		ctx.ResultText(r.pkg)
	case sgIOColVersion:
		ctx.ResultText(r.version)
	case sgIOColArtifact:
		ctx.ResultText(r.artifact)
	case sgIOColRunID:
		ctx.ResultText(r.runID)
	case sgIOColActionID:
		ctx.ResultInt64(r.actionID)
	case sgIOColResourceDigest:
		ctx.ResultText(r.resourceDigest)
	case sgIOColDirection:
		ctx.ResultText(r.direction)
	case sgIOColIOType:
		ctx.ResultText(r.ioType)
	case sgIOColTime:
		ctx.ResultText(r.time)
	case sgIOColTotalSize:
		if r.totalSize > 0 {
			ctx.ResultInt64(r.totalSize)
		} else {
			ctx.ResultNull()
		}
	case sgIOColBytesUsed:
		if r.bytesUsed > 0 {
			ctx.ResultInt64(r.bytesUsed)
		} else {
			ctx.ResultNull()
		}
	case sgIOColResourceType:
		ctx.ResultText(r.resourceType)
	case sgIOColPath:
		if r.path != "" {
			ctx.ResultText(r.path)
		} else {
			ctx.ResultNull()
		}
	case sgIOColAddress:
		if r.address != "" {
			ctx.ResultText(r.address)
		} else {
			ctx.ResultNull()
		}
	}
	return nil
}
