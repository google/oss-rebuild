// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/ncruces/go-sqlite3"
)

// Column indices for the runs table.
const (
	runsColID            = 0
	runsColBenchmark     = 1
	runsColBenchmarkHash = 2
	runsColType          = 3
	runsColCreated       = 4
)

type runsTable struct {
	reader rundex.Reader
	ctx    context.Context
}

type runsCursor struct {
	table *runsTable
	rows  []rundex.Run
	pos   int
}

func registerRunsVTab(db *sqlite3.Conn, ctx context.Context, reader rundex.Reader) error {
	return sqlite3.CreateModule(db, "runs", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*runsTable, error) {
			err := db.DeclareVTab(`CREATE TABLE runs (
				id            TEXT,
				benchmark     TEXT,
				benchmark_hash TEXT,
				type          TEXT,
				created       TEXT
			)`)
			if err != nil {
				return nil, err
			}
			return &runsTable{reader: reader, ctx: ctx}, nil
		})
}

func (t *runsTable) BestIndex(idx *sqlite3.IndexInfo) error {
	// No pushdown for now; fetch all runs.
	idx.EstimatedCost = 1000
	idx.EstimatedRows = 100
	return nil
}

func (t *runsTable) Open() (sqlite3.VTabCursor, error) {
	return &runsCursor{table: t}, nil
}

func (c *runsCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	runs, err := c.table.reader.FetchRuns(c.table.ctx, rundex.FetchRunsOpts{})
	if err != nil {
		return err
	}
	c.rows = runs
	c.pos = 0
	return nil
}

func (c *runsCursor) Next() error {
	c.pos++
	return nil
}

func (c *runsCursor) EOF() bool {
	return c.pos >= len(c.rows)
}

func (c *runsCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case runsColID:
		ctx.ResultText(r.ID)
	case runsColBenchmark:
		ctx.ResultText(r.BenchmarkName)
	case runsColBenchmarkHash:
		ctx.ResultText(r.BenchmarkHash)
	case runsColType:
		ctx.ResultText(string(r.Type))
	case runsColCreated:
		ctx.ResultText(r.Created.Format("2006-01-02T15:04:05Z"))
	}
	return nil
}

func (c *runsCursor) RowID() (int64, error) {
	return int64(c.pos), nil
}
