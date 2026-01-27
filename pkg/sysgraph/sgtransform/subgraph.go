// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"fmt"
	"sync"

	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"google.golang.org/protobuf/proto"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// SubSysGraph is a sysgraph that is a subset of another sysgraph.
type SubSysGraph struct {
	SysGraph
	entrypoints []int64
	ds          sgquery.TransitiveDeps
	resOnce     sync.Once
	res         map[pbdigest.Digest]*sgpb.Resource
	resErr      error
	dgsOnce     sync.Once
	dgs         []pbdigest.Digest
	dgsErr      error
}

var _ SysGraph = (*SubSysGraph)(nil)

// Action returns the action with the given id.
func (sg *SubSysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	a, ok := sg.ds.Actions[id]
	if !ok {
		return nil, fmt.Errorf("action %d not found", id)
	}
	// Remove dangling parent references.
	if _, ok := sg.ds.Actions[a.GetParentActionId()]; a.HasParentActionId() && !ok {
		a = proto.Clone(a).(*sgpb.Action)
		a.ClearParentActionId()
		a.ClearParent()
	}
	return a, nil
}

// ActionIDs returns the ids of all actions in the sysgraph.
func (sg *SubSysGraph) ActionIDs(ctx context.Context) ([]int64, error) {
	return slices.Collect(maps.Keys(sg.ds.Actions)), nil
}

// Resource returns the resource with the given id.
func (sg *SubSysGraph) Resource(ctx context.Context, digest pbdigest.Digest) (*sgpb.Resource, error) {
	if _, ok := sg.ds.Resources[digest]; !ok {
		return nil, fmt.Errorf("resource with digest %s not found", digest)
	}
	r, err := sg.SysGraph.Resource(ctx, digest)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ResourceDigests returns the digests of all resources in the sysgraph.
func (sg *SubSysGraph) ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error) {
	return slices.Collect(maps.Keys(sg.ds.Resources)), nil
}

// Resources returns all resources in the sysgraph.
func (sg *SubSysGraph) Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error) {
	return maps.Clone(sg.ds.Resources), nil
}

// Proto returns the proto representation of the sysgraph.
func (sg *SubSysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	pb := proto.Clone(sg.SysGraph.Proto(ctx)).(*sgpb.SysGraph)
	pb.SetEntryPointActionIds(sg.entrypoints)
	return pb
}

// Subgraphs returns a map of subgraphs keyed by process id.
func Subgraphs(ctx context.Context, sg SysGraph) (map[int64]*SubSysGraph, error) {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	return SubgraphsSome(ctx, sg, aids)
}

// SubgraphsSome returns a map of subgraphs keyed by process id.
func SubgraphsSome(ctx context.Context, sg SysGraph, ids []int64) (map[int64]*SubSysGraph, error) {
	cs, err := sgquery.AllTransitiveDeps(ctx, sg, ids)
	if err != nil {
		return nil, err
	}
	sgs := make(map[int64]*SubSysGraph, len(ids))
	for _, k := range ids {
		sgs[k] = &SubSysGraph{SysGraph: sg, ds: cs[k], entrypoints: []int64{k}}
	}
	return sgs, nil
}

// SubgraphForRoots returns a subgraph for the given roots.
// The subgraph will only contain the given roots and their transitive dependencies.
// If one of the roots is a child of another root, then it will not be an entry point of the subgraph.
func SubgraphForRoots(ctx context.Context, sg SysGraph, ids []int64) (*SubSysGraph, error) {
	cs, err := sgquery.AllTransitiveDeps(ctx, sg, ids)
	if err != nil {
		return nil, err
	}
	for id := range cs {
		isChild := false
		for otherID, otherDeps := range cs {
			if id == otherID {
				continue
			}
			if _, ok := otherDeps.Actions[id]; ok {
				isChild = true
				break
			}
		}
		if isChild {
			delete(cs, id)
		}
	}
	mergedDeps := sgquery.TransitiveDeps{
		Actions:   map[int64]*sgpb.Action{},
		Resources: map[pbdigest.Digest]*sgpb.Resource{},
	}
	for _, deps := range cs {
		maps.Copy(mergedDeps.Actions, deps.Actions)
		maps.Copy(mergedDeps.Resources, deps.Resources)
	}
	return &SubSysGraph{SysGraph: sg, ds: mergedDeps, entrypoints: slices.Collect(maps.Keys(cs))}, nil
}

// FilterForRoot returns a subgraph for the first action in the sysgraph that matches th given filter.
// The filter is applied in a breadth first search starting from the entry points of the sysgraph.
// The subgraph will only contain the matched root and their transitive dependencies.
func FilterForRoot(ctx context.Context, sg SysGraph, filter func(ctx context.Context, a *sgpb.Action) bool) (SysGraph, error) {
	rootAction, err := sgquery.FindFirstBFS(ctx, sg, sg.Proto(ctx).GetEntryPointActionIds(), filter)
	if err != nil {
		return nil, err
	}
	sg, err = SubgraphForRoots(ctx, sg, []int64{rootAction.GetId()})
	if err != nil {
		return nil, err
	}
	sg, _, err = Dense(ctx, sg, 1)
	if err != nil {
		return nil, err
	}
	return sg, nil
}
