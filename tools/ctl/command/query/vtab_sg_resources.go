// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"github.com/ncruces/go-sqlite3"
)

const (
	sgResColEcosystem  = 0
	sgResColPackage    = 1
	sgResColVersion    = 2
	sgResColArtifact   = 3
	sgResColRunID      = 4
	sgResColDigest     = 5
	sgResColType       = 6
	sgResColPath       = 7
	sgResColFileType   = 8
	sgResColFileDigest = 9
	sgResColAddress    = 10
	sgResColProtocol   = 11
)

const (
	sgResHasEcosystem = 1 << iota
	sgResHasPackage
	sgResHasVersion
	sgResHasArtifact
	sgResHasRunID
	sgResAllKeys = sgResHasEcosystem | sgResHasPackage | sgResHasVersion | sgResHasArtifact | sgResHasRunID
)

type sgResourcesTable struct {
	cache *sgCache
}

type sgResourcesCursor struct {
	table *sgResourcesTable
	rows  []sgResourcesRow
	pos   int
}

func registerSGResourcesVTab(db *sqlite3.Conn, cache *sgCache) error {
	return sqlite3.CreateModule(db, "sg_resources", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*sgResourcesTable, error) {
			err := db.DeclareVTab(`CREATE TABLE sg_resources (
				ecosystem   TEXT,
				package     TEXT,
				version     TEXT,
				artifact    TEXT,
				run_id      TEXT,
				digest      TEXT,
				type        TEXT,
				path        TEXT,
				file_type   TEXT,
				file_digest TEXT,
				address     TEXT,
				protocol    TEXT
			)`)
			if err != nil {
				return nil, err
			}
			return &sgResourcesTable{cache: cache}, nil
		})
}

func (t *sgResourcesTable) BestIndex(idx *sqlite3.IndexInfo) error {
	return sgBestIndex(idx, sgResColEcosystem, sgResColPackage, sgResColVersion, sgResColArtifact, sgResColRunID, sgResHasEcosystem, sgResHasPackage, sgResHasVersion, sgResHasArtifact, sgResHasRunID, sgResAllKeys, 10, 5000)
}

func (t *sgResourcesTable) Open() (sqlite3.VTabCursor, error) {
	return &sgResourcesCursor{table: t}, nil
}

func (c *sgResourcesCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	c.rows = nil
	c.pos = 0
	if idxNum&sgResAllKeys != sgResAllKeys {
		return nil
	}
	vals := decodeIdxStr(idxStr, arg)
	entry, err := c.table.cache.load(
		vals[sgResColEcosystem], vals[sgResColPackage],
		vals[sgResColVersion], vals[sgResColArtifact], vals[sgResColRunID])
	if err != nil {
		return err
	}
	c.rows = entry.resourceRows
	return nil
}

func (c *sgResourcesCursor) Next() error           { c.pos++; return nil }
func (c *sgResourcesCursor) EOF() bool             { return c.pos >= len(c.rows) }
func (c *sgResourcesCursor) RowID() (int64, error) { return int64(c.pos), nil }

func (c *sgResourcesCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case sgResColEcosystem:
		ctx.ResultText(r.ecosystem)
	case sgResColPackage:
		ctx.ResultText(r.pkg)
	case sgResColVersion:
		ctx.ResultText(r.version)
	case sgResColArtifact:
		ctx.ResultText(r.artifact)
	case sgResColRunID:
		ctx.ResultText(r.runID)
	case sgResColDigest:
		ctx.ResultText(r.digest)
	case sgResColType:
		ctx.ResultText(r.resType)
	case sgResColPath:
		if r.path != "" {
			ctx.ResultText(r.path)
		} else {
			ctx.ResultNull()
		}
	case sgResColFileType:
		if r.fileType != "" {
			ctx.ResultText(r.fileType)
		} else {
			ctx.ResultNull()
		}
	case sgResColFileDigest:
		if r.fileDigest != "" {
			ctx.ResultText(r.fileDigest)
		} else {
			ctx.ResultNull()
		}
	case sgResColAddress:
		if r.address != "" {
			ctx.ResultText(r.address)
		} else {
			ctx.ResultNull()
		}
	case sgResColProtocol:
		if r.protocol != "" {
			ctx.ResultText(r.protocol)
		} else {
			ctx.ResultNull()
		}
	}
	return nil
}
