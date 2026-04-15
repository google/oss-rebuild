// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgtransform provides transforms for sysgraphs.
package sgtransform

import (
	"context"
	"fmt"
	"slices"

	"log"
	"maps"

	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

// SysGraph is a common interface for working with sysgraphs.
// All transforms should maintain this interface.
type SysGraph interface {
	Proto(ctx context.Context) *sgpb.SysGraph
	Action(ctx context.Context, id int64) (*sgpb.Action, error)
	Resource(ctx context.Context, digest pbdigest.Digest) (*sgpb.Resource, error)
	ActionIDs(ctx context.Context) ([]int64, error)
	// All digests of resources in the sysgraph.
	Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error)
	ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error)
	RawEvents(ctx context.Context) ([]*anypb.Any, error)
	RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error)
	Close() error
}

// Ensure that some common implementations of SysGraph actually implement the interface.
var _ SysGraph = (*inmemory.SysGraph)(nil)
var _ SysGraph = (*sgstorage.DiskSysGraph)(nil)

// DenseSysGraph is a sysgraph with dense action ids.
type DenseSysGraph struct {
	original            SysGraph
	fromDenseToOriginal map[int64]int64
	fromOriginalToDense map[int64]int64
}

var _ SysGraph = (*DenseSysGraph)(nil)

// Dense creates a new sysgraph with dense action ids starting at start.
func Dense(ctx context.Context, sg SysGraph, start int64) (*DenseSysGraph, int64, error) {
	originalAids, err := sg.ActionIDs(ctx)
	log.Printf("originalAids: %v\n", originalAids)
	fromDenseToOriginal := make(map[int64]int64, len(originalAids))
	fromOriginalToDense := make(map[int64]int64, len(originalAids))
	if err != nil {
		return nil, 0, err
	}
	slices.Sort(originalAids)

	for i, aid := range originalAids {
		fromDenseToOriginal[int64(i)+start] = aid
		fromOriginalToDense[aid] = int64(i) + start
	}
	log.Printf("fromDenseToOriginal: %v\n", fromDenseToOriginal)
	return &DenseSysGraph{sg, fromDenseToOriginal, fromOriginalToDense}, int64(len(fromOriginalToDense)), nil
}

// Proto returns the proto representation of the sysgraph.
func (d *DenseSysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	pb := proto.Clone(d.original.Proto(ctx)).(*sgpb.SysGraph)
	for i := range pb.GetEntryPointActionIds() {
		pb.GetEntryPointActionIds()[i] = d.fromOriginalToDense[pb.GetEntryPointActionIds()[i]]
	}
	return pb
}

// ActionIDs returns the ids of all actions in the sysgraph.
func (d *DenseSysGraph) ActionIDs(ctx context.Context) ([]int64, error) {
	return slices.Collect(maps.Keys(d.fromDenseToOriginal)), nil
}

// Action returns the action with the given id.
func (d *DenseSysGraph) Action(ctx context.Context, denseID int64) (*sgpb.Action, error) {
	originalID, ok := d.fromDenseToOriginal[denseID]
	if !ok {
		return nil, fmt.Errorf("action %d not found", denseID)
	}
	a, err := d.original.Action(ctx, originalID)
	if err != nil {
		return nil, err
	}
	a = proto.Clone(a).(*sgpb.Action)
	a.SetId(denseID)
	if a.HasParentActionId() {
		a.SetParentActionId(d.fromOriginalToDense[a.GetParentActionId()])
	}
	originalChildren := a.GetChildren()
	newChildren := make(map[int64]*sgpb.ActionInteraction, len(originalChildren))
	for originalChildIDs, ris := range originalChildren {
		newChildID, ok := d.fromOriginalToDense[originalChildIDs]
		if !ok {
			return nil, fmt.Errorf("child action %d not found", originalChildIDs)
		}
		newChildren[newChildID] = ris
	}
	a.SetChildren(newChildren)
	return a, nil
}

// DenseActionID returns the dense action id for the given action id.
func (d *DenseSysGraph) DenseActionID(originalID int64) int64 {
	return d.fromOriginalToDense[originalID]
}

// RawEventsForAction returns the raw events for the given action id.
func (d *DenseSysGraph) RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error) {
	return d.original.RawEventsForAction(ctx, d.fromDenseToOriginal[id])
}

// Close closes the sysgraph.
func (d *DenseSysGraph) Close() error {
	return d.original.Close()
}

// RawEvents returns all raw events in the sysgraph.
func (d *DenseSysGraph) RawEvents(ctx context.Context) ([]*anypb.Any, error) {
	return d.original.RawEvents(ctx)
}

// Resources returns all resources in the sysgraph.
func (d *DenseSysGraph) Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error) {
	return d.original.Resources(ctx)
}

// Resource returns the resource with the given digest.
func (d *DenseSysGraph) Resource(ctx context.Context, digest pbdigest.Digest) (*sgpb.Resource, error) {
	return d.original.Resource(ctx, digest)
}

// ResourceDigests returns the digests of all resources in the sysgraph.
func (d *DenseSysGraph) ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error) {
	return d.original.ResourceDigests(ctx)
}

// Load loads a sysgraph into memory.
func Load(ctx context.Context, sg SysGraph) (*inmemory.SysGraph, error) {
	var res inmemory.SysGraph
	res.GraphPb = sg.Proto(ctx)
	var err error
	res.Actions, err = sgquery.MapAllActions(ctx, sg,
		func(a *sgpb.Action) (int64, *sgpb.Action, error) {
			return a.GetId(), a, nil
		},
	)
	if err != nil {
		return nil, err
	}
	res.ResourceMap, err = sg.Resources(ctx)
	if err != nil {
		return nil, err
	}
	res.Events = make(map[int64][]*anypb.Any, len(res.Actions))
	for id := range res.Actions {
		events, err := sg.RawEventsForAction(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			res.Events[id] = events
		}
	}
	return &res, nil
}
