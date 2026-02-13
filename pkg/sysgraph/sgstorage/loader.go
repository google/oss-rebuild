// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"archive/zip"
	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

// LoadSysGraph loads the sysgraph from the given proto definition.
func LoadSysGraph(ctx context.Context, path string) (*DiskSysGraph, error) {
	if strings.HasPrefix(path, "gs:") {
		if strings.HasSuffix(path, ".zip") {
			return loadFromGCSZip(ctx, path)
		}
		return loadFromGCS(ctx, path)
	}

	if strings.HasSuffix(path, ".zip") {
		return loadFromZip(ctx, path)
	}
	return loadFromDir(ctx, path)
}

type ctxFS interface {
	ReadFile(ctx context.Context, name string) ([]byte, error)
	ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error)
}

// nonCtxFS is a wrapper around a fs.FS that implements ctxFS.
// It throws away the provided context as that is not supported by fs.FS.
type nonCtxFS struct {
	fs.FS
}

func (f *nonCtxFS) ReadFile(ctx context.Context, name string) ([]byte, error) {
	return fs.ReadFile(f.FS, name)
}

func (f *nonCtxFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.FS, name)
}

func (f *nonCtxFS) Close() error {
	if closer, ok := f.FS.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func loadFromFS(ctx context.Context, sgfs ctxFS) (*DiskSysGraph, error) {
	blob, err := sgfs.ReadFile(ctx, GraphProtoFileName+".pb")
	if err != nil {
		return nil, fmt.Errorf("failed to read graph.pb: %w", err)
	}
	sg := &sgpb.SysGraph{}
	if err := proto.Unmarshal(blob, sg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph.pb: %w", err)
	}
	// If there's Subgraphs, then this is a multi-step sysgraph.
	if len(sg.GetSubgraphs()) > 0 {
		return loadMultiGraph(ctx, sgfs, sg)
	}

	resources, err := loadResourceDB(ctx, sgfs)
	if err != nil {
		return nil, err
	}
	return &DiskSysGraph{
		fs:      sgfs,
		graphPb: sg,
		actionIds: sync.OnceValues(func() ([]int64, error) {
			return loadActionIds(ctx, sgfs, 0)
		}),
		resources: resources,
		sf:        &singleflight.Group{},
	}, nil
}

func loadMultiGraph(ctx context.Context, sgfs ctxFS, sg *sgpb.SysGraph) (*DiskSysGraph, error) {
	// Verify that base graph has no actions.
	aids, _ := loadActionIds(ctx, sgfs, 0) // Ignore errors as we're only checking for the presence of actions.
	if len(aids) > 0 {
		return nil, fmt.Errorf("base graph has %d actions, multi-step graphs must have an empty base graph", len(aids))
	}

	Subgraphsize := make(map[string]int64) // Size of each susgraph in number of actions.
	var allActionIDs []int64
	var allEntryPointIDs []int64
	allResources := make(map[pbdigest.Digest]*sgpb.Resource)
	offset := int64(0)
	for _, p := range sg.GetSubgraphs() {
		nfs, ok := sgfs.(*nonCtxFS)
		if !ok {
			return nil, errors.New("multi graph loading requires a non-cancellable FS")
		}
		// Create a new fs.FS that points to the susgraph directory.
		subfs, err := fs.Sub(nfs.FS, p)
		if err != nil {
			return nil, fmt.Errorf("failed to create sub-filesystem for %q: %w", p, err)
		}
		// Load the susgraph as a single graph.
		sg, err := loadFromFS(ctx, &nonCtxFS{subfs})
		if err != nil {
			return nil, fmt.Errorf("failed to load susgraph from %q: %w", p, err)
		}
		// Add offsetted entry point IDs to the main graph.
		for _, entryPointID := range sg.graphPb.GetEntryPointActionIds() {
			allEntryPointIDs = append(allEntryPointIDs, entryPointID+offset)
		}
		// Add offsetted action IDs to the main graph.
		aids, err := sg.actionIds()
		if err != nil {
			return nil, fmt.Errorf("failed to load action ids from %q: %w", p, err)
		}
		for _, aid := range aids {
			allActionIDs = append(allActionIDs, aid+offset)
		}
		Subgraphsize[p] = int64(len(aids))
		// Gather resources.
		resources, err := sg.Resources(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load resources from %q: %w", p, err)
		}
		for dg, r := range resources {
			allResources[dg] = proto.Clone(r).(*sgpb.Resource)
		}
		// Update the offset for the next susgraph.
		offset += Subgraphsize[p]
	}
	multisg := sgpb.SysGraph_builder{
		Id:                  proto.String(sg.GetId()),
		EntryPointActionIds: allEntryPointIDs,
		Subgraphs:           sg.GetSubgraphs(),
	}.Build()

	g := &DiskSysGraph{
		fs:           sgfs,
		graphPb:      multisg,
		actionIds:    func() ([]int64, error) { return allActionIDs, nil },
		resources:    allResources,
		Subgraphsize: Subgraphsize,
		sf:           &singleflight.Group{},
	}
	return g, nil
}

func loadFromZip(ctx context.Context, path string) (*DiskSysGraph, error) {
	fs, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	return loadFromFS(ctx, &nonCtxFS{fs})
}

func loadFromDir(ctx context.Context, path string) (*DiskSysGraph, error) {
	return loadFromFS(ctx, &nonCtxFS{os.DirFS(path)})
}

func loadFromGCSZip(ctx context.Context, path string) (*DiskSysGraph, error) {
	// Copy the zip file to local disk as we cannot do random access of the file on GCS.
	localZipFile, err := copyFromGCS(ctx, path)
	if err != nil {
		return nil, err
	}
	sg, err := loadFromZip(ctx, localZipFile)
	if err != nil {
		return nil, err
	}
	sg.cleanupFns = append(sg.cleanupFns, func() error {
		return os.Remove(localZipFile)
	})
	return sg, nil
}

func loadFromGCS(ctx context.Context, path string) (*DiskSysGraph, error) {
	fs, err := newGCSFS(path)
	if err != nil {
		return nil, err
	}
	return loadFromFS(ctx, fs)
}

func loadResourceDB(ctx context.Context, sgfs ctxFS) (map[pbdigest.Digest]*sgpb.Resource, error) {
	blob, err := sgfs.ReadFile(ctx, RDBProtoFileName+".pb")
	if err != nil {
		return nil, fmt.Errorf("failed to read resource db proto: %v", err)
	}
	resourceDB := &sgpb.ResourceDB{}
	if err := proto.Unmarshal(blob, resourceDB); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resource db proto: %v", err)
	}
	resources := make(map[pbdigest.Digest]*sgpb.Resource, len(resourceDB.GetResources()))
	for strDg, r := range resourceDB.GetResources() {
		dg, err := pbdigest.NewFromString(strDg)
		if err != nil {
			return nil, fmt.Errorf("failed to parse resource digest %s: %v", strDg, err)
		}
		resources[dg] = r
	}
	return resources, nil
}

// DiskSysGraph is a sysgraph loaded from disk.
type DiskSysGraph struct {
	sf           *singleflight.Group
	fs           ctxFS
	cleanupFns   []func() error
	graphPbOnce  sync.Once
	graphPb      *sgpb.SysGraph
	Subgraphsize map[string]int64 // Subdirectory to size of susgraph in number of actions.
	actionIds    func() ([]int64, error)
	resources    map[pbdigest.Digest]*sgpb.Resource
	actionCache  singleflightCache[*sgpb.Action]
}

// Action returns the action with the given id.
func (sg *DiskSysGraph) action(ctx context.Context, id int64) (*sgpb.Action, error) {
	p, err := url.JoinPath(ActionDirName, ActionFileName(id)+".pb")
	if err != nil {
		return nil, err
	}
	blob, err := sg.fs.ReadFile(ctx, p)
	if err != nil {
		return nil, err
	}
	a := &sgpb.Action{}
	if err := proto.Unmarshal(blob, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (sg *DiskSysGraph) multiAction(ctx context.Context, id int64) (*sgpb.Action, error) {
	// Figure out what directory to look in.
	var dir string
	var cumulativeSize int64
	var offset int64
	Subgraphs := sg.graphPb.GetSubgraphs()
	for _, sub := range Subgraphs {
		cumulativeSize += sg.Subgraphsize[sub]
		if id <= cumulativeSize {
			dir = sub
			break
		}
		offset += sg.Subgraphsize[sub]
	}
	if dir == "" {
		return nil, fmt.Errorf("action %d not found un multi-step graph", id)
	}

	// Calculate the offset of the action ID within the susgraph directory.
	id = id - offset
	// Read the action file.
	p, err := url.JoinPath(dir, ActionDirName, ActionFileName(id)+".pb")
	if err != nil {
		return nil, err
	}
	blob, err := sg.fs.ReadFile(ctx, p)
	if err != nil {
		return nil, err
	}
	a := &sgpb.Action{}
	if err := proto.Unmarshal(blob, a); err != nil {
		return nil, err
	}

	// Remap the action ID, parent action ID, and child action IDs to be relative to the global graph.
	a.SetId(a.GetId() + offset)
	childMap := make(map[int64]*sgpb.ActionInteraction, len(a.GetChildren()))
	for c, ri := range a.GetChildren() {
		childMap[c+offset] = ri
	}
	a.SetChildren(childMap)
	if a.GetParentActionId() > 0 {
		a.SetParentActionId(a.GetParentActionId() + offset)
	}

	return a, nil
}

// Action returns the action with the given global id.
func (sg *DiskSysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	return sg.actionCache.load(ctx, strconv.FormatInt(id, 10), func() (*sgpb.Action, error) {
		if len(sg.graphPb.GetSubgraphs()) > 0 {
			return sg.multiAction(ctx, id)
		}
		return sg.action(ctx, id)
	})
}

type singleflightCache[V any] struct {
	sf    singleflight.Group
	cache sync.Map
}

func (sfc *singleflightCache[V]) load(ctx context.Context, cacheID string, load func() (V, error)) (V, error) {
	if r, ok := sfc.cache.Load(cacheID); ok {
		return r.(V), nil
	}
	var nilT V
	aAny, err, shared := sfc.sf.Do(cacheID, func() (any, error) {
		a, err := load()
		if err != nil {
			return nil, err
		}
		sfc.cache.Store(cacheID, a)
		return a, nil
	})
	if shared && err != nil && ctx.Err() == nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Canceled {
			return sfc.load(ctx, cacheID, load)
		}
	}
	if err != nil {
		return nilT, err
	}
	if aAny == nil {
		return nilT, err
	}
	return aAny.(V), err
}

// Close closes the sysgraph.
func (sg *DiskSysGraph) Close() error {
	if closer, ok := sg.fs.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			return err
		}
	}
	for _, fn := range sg.cleanupFns {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

// Resource returns the resource with the given id.
func (sg *DiskSysGraph) Resource(ctx context.Context, id pbdigest.Digest) (*sgpb.Resource, error) {
	if r, ok := sg.resources[id]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("resource %s not found", id)
}

// ActionIDs returns the ids of all actions in the sysgraph.
func (sg *DiskSysGraph) ActionIDs(ctx context.Context) ([]int64, error) {
	return sg.actionIds()
}

// ActionIDs returns the ids of all actions in the sysgraph.
func loadActionIds(ctx context.Context, sgfs ctxFS, offset int64) ([]int64, error) {
	var dirEntries []fs.DirEntry
	var err error
	dirEntries, err = sgfs.ReadDir(ctx, ActionDirName)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var errs []error
	for _, d := range dirEntries {
		name := d.Name()
		if filepath.Ext(name) != ".pb" {
			continue // Filter out json and textproto versions of action files.
		}

		id, err := strconv.ParseInt(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), 10, 64)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if id == 0 {
			errs = append(errs, fmt.Errorf("action id 0 is not valid, all action ids must be non-zero"))
			continue
		}
		ids = append(ids, id+offset)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return ids, nil
}

// ResourceDigests returns the digests of all resources in the sysgraph.
func (sg *DiskSysGraph) ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error) {
	return slices.Collect(maps.Keys(sg.resources)), nil
}

// Resources returns all resources in the sysgraph.
func (sg *DiskSysGraph) Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error) {
	return maps.Clone(sg.resources), nil
}

// Proto returns the proto definition of the sysgraph.
func (sg *DiskSysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	return sg.graphPb
}

// RawEvents returns the raw events from tetragon.
func (sg *DiskSysGraph) RawEvents(ctx context.Context) ([]*anypb.Any, error) {
	var events []*anypb.Any
	ids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		actionEvents, err := sg.RawEventsForAction(ctx, id)
		if err != nil {
			return nil, err
		}
		events = append(events, actionEvents...)
	}
	return events, nil
}

// RawEventsForAction returns the raw events for the given action from tetragon.
func (sg *DiskSysGraph) RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error) {
	blob, err := sg.fs.ReadFile(ctx, filepath.Join(ActionDirName, RawActionFileName(id)+".pbdelim"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// If the file does not exist, return an empty list of events.
			return nil, nil
		}
		return nil, err
	}
	var events []*anypb.Any
	r := bytes.NewReader(blob)
	for {
		event := &anypb.Any{}
		if err := protodelim.UnmarshalFrom(r, event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
