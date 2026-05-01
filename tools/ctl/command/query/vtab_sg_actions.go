// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"github.com/ncruces/go-sqlite3"
)

const (
	sgActionsColEcosystem    = 0
	sgActionsColPackage      = 1
	sgActionsColVersion      = 2
	sgActionsColArtifact     = 3
	sgActionsColRunID        = 4
	sgActionsColActionID     = 5
	sgActionsColParentID     = 6
	sgActionsColIsEntryPoint = 7
	sgActionsColCommand      = 8
	sgActionsColExecutable   = 9
	sgActionsColCwd          = 10
	sgActionsColPid          = 11
	sgActionsColStartTime    = 12
	sgActionsColEndTime      = 13
	sgActionsColDurationS    = 14
	sgActionsColIsFork       = 15
	sgActionsColExitStatus   = 16
)

const (
	sgActionsHasEcosystem = 1 << iota
	sgActionsHasPackage
	sgActionsHasVersion
	sgActionsHasArtifact
	sgActionsHasRunID
	sgActionsAllKeys = sgActionsHasEcosystem | sgActionsHasPackage | sgActionsHasVersion | sgActionsHasArtifact | sgActionsHasRunID
)

type sgActionsTable struct {
	cache *sgCache
}

type sgActionsCursor struct {
	table *sgActionsTable
	rows  []sgActionsRow
	pos   int
}

func registerSGActionsVTab(db *sqlite3.Conn, cache *sgCache) error {
	return sqlite3.CreateModule(db, "sg_actions", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*sgActionsTable, error) {
			err := db.DeclareVTab(`CREATE TABLE sg_actions (
				ecosystem      TEXT,
				package        TEXT,
				version        TEXT,
				artifact       TEXT,
				run_id         TEXT,
				action_id      INTEGER,
				parent_id      INTEGER,
				is_entry_point INTEGER,
				command        TEXT,
				executable     TEXT,
				cwd            TEXT,
				pid            INTEGER,
				start_time     TEXT,
				end_time       TEXT,
				duration_s     REAL,
				is_fork        INTEGER,
				exit_status    INTEGER
			)`)
			if err != nil {
				return nil, err
			}
			return &sgActionsTable{cache: cache}, nil
		})
}

func (t *sgActionsTable) BestIndex(idx *sqlite3.IndexInfo) error {
	return sgBestIndex(idx, sgActionsColEcosystem, sgActionsColPackage, sgActionsColVersion, sgActionsColArtifact, sgActionsColRunID, sgActionsHasEcosystem, sgActionsHasPackage, sgActionsHasVersion, sgActionsHasArtifact, sgActionsHasRunID, sgActionsAllKeys, 10, 1000)
}

func (t *sgActionsTable) Open() (sqlite3.VTabCursor, error) {
	return &sgActionsCursor{table: t}, nil
}

func (c *sgActionsCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	c.rows = nil
	c.pos = 0
	if idxNum&sgActionsAllKeys != sgActionsAllKeys {
		return nil
	}
	vals := decodeIdxStr(idxStr, arg)
	entry, err := c.table.cache.load(
		vals[sgActionsColEcosystem], vals[sgActionsColPackage],
		vals[sgActionsColVersion], vals[sgActionsColArtifact], vals[sgActionsColRunID])
	if err != nil {
		return err
	}
	c.rows = entry.actionRows
	return nil
}

func (c *sgActionsCursor) Next() error           { c.pos++; return nil }
func (c *sgActionsCursor) EOF() bool             { return c.pos >= len(c.rows) }
func (c *sgActionsCursor) RowID() (int64, error) { return int64(c.pos), nil }

func (c *sgActionsCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case sgActionsColEcosystem:
		ctx.ResultText(r.ecosystem)
	case sgActionsColPackage:
		ctx.ResultText(r.pkg)
	case sgActionsColVersion:
		ctx.ResultText(r.version)
	case sgActionsColArtifact:
		ctx.ResultText(r.artifact)
	case sgActionsColRunID:
		ctx.ResultText(r.runID)
	case sgActionsColActionID:
		ctx.ResultInt64(r.actionID)
	case sgActionsColParentID:
		ctx.ResultInt64(r.parentID)
	case sgActionsColIsEntryPoint:
		if r.isEntryPoint {
			ctx.ResultInt64(1)
		} else {
			ctx.ResultInt64(0)
		}
	case sgActionsColCommand:
		ctx.ResultText(r.command)
	case sgActionsColExecutable:
		ctx.ResultText(r.executable)
	case sgActionsColCwd:
		ctx.ResultText(r.cwd)
	case sgActionsColPid:
		ctx.ResultInt64(r.pid)
	case sgActionsColStartTime:
		ctx.ResultText(r.startTime)
	case sgActionsColEndTime:
		ctx.ResultText(r.endTime)
	case sgActionsColDurationS:
		ctx.ResultFloat(r.durationS)
	case sgActionsColIsFork:
		if r.isFork {
			ctx.ResultInt64(1)
		} else {
			ctx.ResultInt64(0)
		}
	case sgActionsColExitStatus:
		ctx.ResultInt64(int64(r.exitStatus))
	}
	return nil
}
