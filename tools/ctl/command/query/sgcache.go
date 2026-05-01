// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/ncruces/go-sqlite3"
)

// Sysgraph loading strategy
//
// The current approach pre-materializes sg_actions and sg_resources rows from
// the loaded DiskSysGraph, while sg_io rows are computed lazily per-action
// (via action_id pushdown in BestIndex). This trades memory for query speed:
//
//   Benchmark (14K actions, 62K resources, ~5M I/O interactions):
//     Cached setup:  283ms (zip) + 150ms (materialize) = ~430ms
//     Cached query:  21ms (scan 14K in-memory rows)
//     Heap usage:    ~85 MB
//
// For large sysgraphs (1GB+, 100K+ actions), a streaming approach should be
// considered. The key changes would be:
//
//  1. Replace pre-materialized action rows with a streaming cursor that calls
//     DiskSysGraph.Action() per-row in Column(). The cursor holds only the
//     action ID list and resource map, not row structs.
//
//  2. Setup cost drops to zip load + resource map (~250ms for 14K actions),
//     but each query pays per-row proto deserialization (~2.5s for 14K).
//
//   Streaming benchmark (same graph):
//     Setup:       231ms
//     Query:       2,580ms (per-row Action() from zip)
//     Heap usage:  ~50 MB
//
// The streaming approach is viable when:
//   - The graph is too large to materialize (>1GB)
//   - Only a single query is run per session (no amortization benefit)
//   - Memory is constrained
//
// Implementation: a streaming vtab_sg_actions_stream.go was prototyped and
// benchmarked. It can be reintroduced behind a --sg-stream flag or
// auto-detected based on graph size (e.g. >50K actions).

// sgBestIndex implements the shared BestIndex logic for all sysgraph vtables.
// It requires all 5 key columns to be EQ-constrained.
func sgBestIndex(idx *sqlite3.IndexInfo, colEco, colPkg, colVer, colArt, colRun int, bitEco, bitPkg, bitVer, bitArt, bitRun, allKeys int, costFound, rowsFound int64) error {
	var idxNum int
	argIdx := 1
	var colOrder []byte
	colBits := map[int]int{colEco: bitEco, colPkg: bitPkg, colVer: bitVer, colArt: bitArt, colRun: bitRun}
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
	if idxNum&allKeys == allKeys {
		idx.EstimatedCost = float64(costFound)
		idx.EstimatedRows = rowsFound
	} else {
		idx.EstimatedCost = 1e18
		idx.EstimatedRows = 1e15
	}
	return nil
}

// decodeIdxStr extracts column values from Filter args using the encoding from sgBestIndex.
func decodeIdxStr(idxStr string, arg []sqlite3.Value) map[int]string {
	vals := make(map[int]string, len(idxStr))
	for i, col := range []byte(idxStr) {
		if i < len(arg) {
			vals[int(col)-1] = arg[i].Text()
		}
	}
	return vals
}

// Row types for pre-computed sysgraph data.

type sgActionsRow struct {
	ecosystem    string
	pkg          string
	version      string
	artifact     string
	runID        string
	actionID     int64
	parentID     int64
	isEntryPoint bool
	command      string
	executable   string
	cwd          string
	pid          int64
	startTime    string
	endTime      string
	durationS    float64
	isFork       bool
	exitStatus   int
}

type sgIORow struct {
	ecosystem      string
	pkg            string
	version        string
	artifact       string
	runID          string
	actionID       int64
	resourceDigest string
	direction      string // "input" or "output"
	ioType         string // READ, WRITE, DELETE, RENAME_SOURCE, RENAME_DEST
	time           string
	totalSize      int64
	bytesUsed      int64
	resourceType   string // FILE, NETWORK_ADDRESS, PIPE
	path           string
	address        string
}

type sgResourcesRow struct {
	ecosystem  string
	pkg        string
	version    string
	artifact   string
	runID      string
	digest     string
	resType    string // FILE, NETWORK_ADDRESS, PIPE
	path       string
	fileType   string // REGULAR, DIRECTORY, SYMLINK, SOCKET
	fileDigest string
	address    string
	protocol   string
}

type sgCacheKey struct {
	ecosystem, pkg, version, artifact, runID string
}

type sgCacheEntry struct {
	actionRows   []sgActionsRow
	resourceRows []sgResourcesRow
	// Keep the loaded graph for lazy I/O row computation.
	sg        sgtransform.SysGraph
	resources map[pbdigest.Digest]*sgpb.Resource
	// Key columns for I/O row construction.
	ecosystem, pkg, version, artifact, runID string
}

// ioRowsForAction computes I/O rows for a single action on demand.
func (e *sgCacheEntry) ioRowsForAction(ctx context.Context, actionID int64) ([]sgIORow, error) {
	a, err := e.sg.Action(ctx, actionID)
	if err != nil {
		return nil, err
	}
	var rows []sgIORow
	for digestStr, ris := range a.GetInputs() {
		for _, ri := range ris.GetInteractions() {
			rows = append(rows, makeIORow(
				e.ecosystem, e.pkg, e.version, e.artifact, e.runID,
				a.GetId(), digestStr, "input", ri, e.resources))
		}
	}
	for digestStr, ris := range a.GetOutputs() {
		for _, ri := range ris.GetInteractions() {
			rows = append(rows, makeIORow(
				e.ecosystem, e.pkg, e.version, e.artifact, e.runID,
				a.GetId(), digestStr, "output", ri, e.resources))
		}
	}
	return rows, nil
}

// allIORows computes I/O rows for all actions (fallback for unconstrained scans).
func (e *sgCacheEntry) allIORows(ctx context.Context) ([]sgIORow, error) {
	var allRows []sgIORow
	for _, ar := range e.actionRows {
		rows, err := e.ioRowsForAction(ctx, ar.actionID)
		if err != nil {
			continue
		}
		allRows = append(allRows, rows...)
	}
	return allRows, nil
}

type sgCache struct {
	mu      sync.Mutex
	entries map[sgCacheKey]*sgCacheEntry
	assets  *assetlocator.MetaAssetStore
	ctx     context.Context
}

func newSGCache(ctx context.Context, assets *assetlocator.MetaAssetStore) *sgCache {
	return &sgCache{
		entries: make(map[sgCacheKey]*sgCacheEntry),
		assets:  assets,
		ctx:     ctx,
	}
}

func (c *sgCache) load(ecosystem, pkg, version, artifact, runID string) (*sgCacheEntry, error) {
	key := sgCacheKey{ecosystem, pkg, version, artifact, runID}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()

	entry, err := c.fetch(ecosystem, pkg, version, artifact, runID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
	return entry, nil
}

func (c *sgCache) fetch(ecosystem, pkg, version, artifact, runID string) (*sgCacheEntry, error) {
	target := rebuild.Target{
		Ecosystem: rebuild.Ecosystem(ecosystem),
		Package:   pkg,
		Version:   version,
		Artifact:  artifact,
	}
	store, err := c.assets.For(c.ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("creating asset store: %w", err)
	}
	r, err := store.Reader(c.ctx, rebuild.SysgraphAsset.For(target))
	if err != nil {
		return nil, fmt.Errorf("reading sysgraph asset: %w", err)
	}
	defer r.Close()

	// Write to temp file (LoadSysGraph requires a path for zip random access).
	tmp, err := os.CreateTemp("", "sysgraph-*.zip")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing sysgraph to temp: %w", err)
	}
	tmp.Close()

	sg, err := sgstorage.LoadSysGraph(c.ctx, tmpPath)
	if err != nil {
		return nil, fmt.Errorf("loading sysgraph: %w", err)
	}
	// NOTE: sg is not closed here; it's held by the cache entry for lazy I/O queries.

	return buildCacheEntry(c.ctx, sg, ecosystem, pkg, version, artifact, runID)
}

func buildCacheEntry(ctx context.Context, sg sgtransform.SysGraph, ecosystem, pkg, version, artifact, runID string) (*sgCacheEntry, error) {
	entry := &sgCacheEntry{
		sg:        sg,
		ecosystem: ecosystem,
		pkg:       pkg,
		version:   version,
		artifact:  artifact,
		runID:     runID,
	}

	// Load resources for resolving digests.
	resources, err := sg.Resources(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading resources: %w", err)
	}
	entry.resources = resources

	// Build resource rows.
	for dg, res := range resources {
		entry.resourceRows = append(entry.resourceRows, makeResourceRow(
			ecosystem, pkg, version, artifact, runID, dg.String(), res))
	}

	// Load all action IDs.
	actionIDs, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading action IDs: %w", err)
	}

	entryPoints := make(map[int64]bool)
	for _, id := range sg.Proto(ctx).GetEntryPointActionIds() {
		entryPoints[id] = true
	}

	// Build action rows.
	for _, id := range actionIDs {
		a, err := sg.Action(ctx, id)
		if err != nil {
			continue
		}

		// Resolve executable path.
		execPath := resolveResourcePath(resources, a.GetExecutableResourceDigest())

		// Build action row.
		row := sgActionsRow{
			ecosystem:    ecosystem,
			pkg:          pkg,
			version:      version,
			artifact:     artifact,
			runID:        runID,
			actionID:     a.GetId(),
			parentID:     a.GetParentActionId(),
			isEntryPoint: entryPoints[a.GetId()],
			command:      strings.Join(a.GetExecInfo().GetArgv(), " "),
			executable:   execPath,
			cwd:          a.GetExecInfo().GetWorkingDirectory(),
			pid:          a.GetExecInfo().GetPid(),
			isFork:       a.GetIsFork(),
			exitStatus:   int(a.GetExitStatus()),
		}
		if st := a.GetStartTime(); st != nil {
			row.startTime = st.AsTime().Format("2006-01-02T15:04:05Z")
		}
		if et := a.GetEndTime(); et != nil {
			row.endTime = et.AsTime().Format("2006-01-02T15:04:05Z")
		}
		if st, et := a.GetStartTime(), a.GetEndTime(); st != nil && et != nil {
			row.durationS = et.AsTime().Sub(st.AsTime()).Seconds()
		}
		entry.actionRows = append(entry.actionRows, row)
	}

	return entry, nil
}

func resolveResourcePath(resources map[pbdigest.Digest]*sgpb.Resource, digestStr string) string {
	if digestStr == "" {
		return ""
	}
	dg, err := pbdigest.NewFromString(digestStr)
	if err != nil {
		return ""
	}
	res, ok := resources[dg]
	if !ok {
		return ""
	}
	if fi := res.GetFileInfo(); fi != nil {
		return fi.GetPath()
	}
	return ""
}

func resourceTypeString(rt sgpb.ResourceType) string {
	switch rt {
	case sgpb.ResourceType_RESOURCE_TYPE_FILE:
		return "FILE"
	case sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS:
		return "NETWORK_ADDRESS"
	case sgpb.ResourceType_RESOURCE_TYPE_PIPE:
		return "PIPE"
	default:
		return "UNKNOWN"
	}
}

func fileTypeString(ft sgpb.FileType) string {
	switch ft {
	case sgpb.FileType_FILE_TYPE_REGULAR:
		return "REGULAR"
	case sgpb.FileType_FILE_TYPE_DIRECTORY:
		return "DIRECTORY"
	case sgpb.FileType_FILE_TYPE_SYMLINK:
		return "SYMLINK"
	case sgpb.FileType_FILE_TYPE_SOCKET:
		return "SOCKET"
	default:
		return ""
	}
}

func interactionTypeString(t sgpb.ResourceInteractionType) string {
	switch t {
	case sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_READ:
		return "READ"
	case sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_WRITE:
		return "WRITE"
	case sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_DELETE:
		return "DELETE"
	case sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_RENAME_SOURCE:
		return "RENAME_SOURCE"
	case sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_RENAME_DESTINATION:
		return "RENAME_DEST"
	default:
		return "UNKNOWN"
	}
}

func makeIORow(ecosystem, pkg, version, artifact, runID string, actionID int64, digestStr, direction string, ri *sgpb.ResourceInteraction, resources map[pbdigest.Digest]*sgpb.Resource) sgIORow {
	row := sgIORow{
		ecosystem:      ecosystem,
		pkg:            pkg,
		version:        version,
		artifact:       artifact,
		runID:          runID,
		actionID:       actionID,
		resourceDigest: digestStr,
		direction:      direction,
		ioType:         interactionTypeString(ri.GetType()),
	}
	if ts := ri.GetTimestamp(); ts != nil {
		row.time = ts.AsTime().Format("2006-01-02T15:04:05Z")
	}
	if info := ri.GetIoInfo(); info != nil {
		row.totalSize = int64(info.GetTotalSizeBytes())
		row.bytesUsed = int64(info.GetBytesUsed())
	}
	// Denormalize resource fields.
	if dg, err := pbdigest.NewFromString(digestStr); err == nil {
		if res, ok := resources[dg]; ok {
			row.resourceType = resourceTypeString(res.GetType())
			if fi := res.GetFileInfo(); fi != nil {
				row.path = fi.GetPath()
			}
			if ni := res.GetNetworkAddrInfo(); ni != nil {
				row.address = ni.GetAddress()
			}
		}
	}
	return row
}

func makeResourceRow(ecosystem, pkg, version, artifact, runID, digestStr string, res *sgpb.Resource) sgResourcesRow {
	row := sgResourcesRow{
		ecosystem: ecosystem,
		pkg:       pkg,
		version:   version,
		artifact:  artifact,
		runID:     runID,
		digest:    digestStr,
		resType:   resourceTypeString(res.GetType()),
	}
	if fi := res.GetFileInfo(); fi != nil {
		row.path = fi.GetPath()
		row.fileType = fileTypeString(fi.GetType())
		row.fileDigest = fi.GetDigest()
	}
	if ni := res.GetNetworkAddrInfo(); ni != nil {
		row.address = ni.GetAddress()
		row.protocol = ni.GetProtocol()
	}
	return row
}
