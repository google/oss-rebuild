// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"
	"encoding/json"
	"io"

	"github.com/google/oss-rebuild/internal/netclassify"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/ncruces/go-sqlite3"
)

// Column indices for the netlog table.
const (
	netlogColEcosystem = 0
	netlogColPackage   = 1
	netlogColVersion   = 2
	netlogColArtifact  = 3
	netlogColRunID     = 4
	netlogColMethod    = 5
	netlogColScheme    = 6
	netlogColHost      = 7
	netlogColPath      = 8
	netlogColURL       = 9
	netlogColPURL      = 10
	netlogColTime      = 11
)

// BestIndex bitmask flags for required key columns.
const (
	netlogHasEcosystem = 1 << iota
	netlogHasPackage
	netlogHasVersion
	netlogHasArtifact
	netlogHasRunID
	netlogAllKeys = netlogHasEcosystem | netlogHasPackage | netlogHasVersion | netlogHasArtifact | netlogHasRunID
)

type netlogTable struct {
	assets *assetlocator.MetaAssetStore
	ctx    context.Context
}

type netlogCursor struct {
	table *netlogTable
	rows  []netlogRow
	pos   int
}

type netlogRow struct {
	ecosystem string
	pkg       string
	version   string
	artifact  string
	runID     string
	method    string
	scheme    string
	host      string
	path      string
	url       string
	purl      string
	time      string
}

func registerNetlogVTab(db *sqlite3.Conn, ctx context.Context, assets *assetlocator.MetaAssetStore) error {
	return sqlite3.CreateModule(db, "netlog", nil,
		func(db *sqlite3.Conn, _, _, _ string, _ ...string) (*netlogTable, error) {
			err := db.DeclareVTab(`CREATE TABLE netlog (
				ecosystem TEXT,
				package   TEXT,
				version   TEXT,
				artifact  TEXT,
				run_id    TEXT,
				method    TEXT,
				scheme    TEXT,
				host      TEXT,
				path      TEXT,
				url       TEXT,
				purl      TEXT,
				time      TEXT
			)`)
			if err != nil {
				return nil, err
			}
			return &netlogTable{assets: assets, ctx: ctx}, nil
		})
}

func (t *netlogTable) BestIndex(idx *sqlite3.IndexInfo) error {
	var idxNum int
	argIdx := 1
	var colOrder []byte
	for i, c := range idx.Constraint {
		if !c.Usable || c.Op != sqlite3.INDEX_CONSTRAINT_EQ {
			continue
		}
		var bit int
		switch c.Column {
		case netlogColEcosystem:
			bit = netlogHasEcosystem
		case netlogColPackage:
			bit = netlogHasPackage
		case netlogColVersion:
			bit = netlogHasVersion
		case netlogColArtifact:
			bit = netlogHasArtifact
		case netlogColRunID:
			bit = netlogHasRunID
		default:
			continue
		}
		if idxNum&bit != 0 {
			continue
		}
		idx.ConstraintUsage[i].ArgvIndex = argIdx
		idx.ConstraintUsage[i].Omit = true
		idxNum |= bit
		colOrder = append(colOrder, byte(c.Column)+1) // +1 to avoid null bytes
		argIdx++
	}
	idx.IdxNum = idxNum
	idx.IdxStr = string(colOrder)
	if idxNum&netlogAllKeys == netlogAllKeys {
		idx.EstimatedCost = 10
		idx.EstimatedRows = 1000
	} else {
		idx.EstimatedCost = 1e18
		idx.EstimatedRows = 1e15
	}
	return nil
}

func (t *netlogTable) Open() (sqlite3.VTabCursor, error) {
	return &netlogCursor{table: t}, nil
}

func (c *netlogCursor) Filter(idxNum int, idxStr string, arg ...sqlite3.Value) error {
	c.rows = nil
	c.pos = 0
	if idxNum&netlogAllKeys != netlogAllKeys {
		return nil
	}
	vals := make(map[int]string, len(idxStr))
	for i, col := range []byte(idxStr) {
		if i < len(arg) {
			vals[int(col)-1] = arg[i].Text()
		}
	}
	target := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(vals[netlogColEcosystem]),
		Package:   vals[netlogColPackage],
		Version:   vals[netlogColVersion],
		Artifact:  vals[netlogColArtifact],
	}
	runID := vals[netlogColRunID]
	store, err := c.table.assets.For(c.table.ctx, runID)
	if err != nil {
		return err
	}
	r, err := store.Reader(c.table.ctx, rebuild.ProxyNetlogAsset.For(target))
	if err != nil {
		return err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var nal netlog.NetworkActivityLog
	if err := json.Unmarshal(data, &nal); err != nil {
		return err
	}
	eco := string(target.Ecosystem)
	for _, req := range nal.HTTPRequests {
		fullURL := req.Scheme + "://" + req.Host + req.Path
		purl, _ := netclassify.ClassifyURL(fullURL)
		row := netlogRow{
			ecosystem: eco,
			pkg:       target.Package,
			version:   target.Version,
			artifact:  target.Artifact,
			runID:     runID,
			method:    req.Method,
			scheme:    req.Scheme,
			host:      req.Host,
			path:      req.Path,
			url:       fullURL,
			purl:      purl,
		}
		if !req.Time.IsZero() {
			row.time = req.Time.Format("2006-01-02T15:04:05Z")
		}
		c.rows = append(c.rows, row)
	}
	return nil
}

func (c *netlogCursor) Next() error {
	c.pos++
	return nil
}

func (c *netlogCursor) EOF() bool {
	return c.pos >= len(c.rows)
}

func (c *netlogCursor) Column(ctx sqlite3.Context, col int) error {
	r := c.rows[c.pos]
	switch col {
	case netlogColEcosystem:
		ctx.ResultText(r.ecosystem)
	case netlogColPackage:
		ctx.ResultText(r.pkg)
	case netlogColVersion:
		ctx.ResultText(r.version)
	case netlogColArtifact:
		ctx.ResultText(r.artifact)
	case netlogColRunID:
		ctx.ResultText(r.runID)
	case netlogColMethod:
		ctx.ResultText(r.method)
	case netlogColScheme:
		ctx.ResultText(r.scheme)
	case netlogColHost:
		ctx.ResultText(r.host)
	case netlogColPath:
		ctx.ResultText(r.path)
	case netlogColURL:
		ctx.ResultText(r.url)
	case netlogColPURL:
		if r.purl == "" {
			ctx.ResultNull()
		} else {
			ctx.ResultText(r.purl)
		}
	case netlogColTime:
		ctx.ResultText(r.time)
	}
	return nil
}

func (c *netlogCursor) RowID() (int64, error) {
	return int64(c.pos), nil
}
