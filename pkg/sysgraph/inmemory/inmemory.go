// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package inmemory provides an in-memory implementation of SysGraph.
package inmemory

import (
	"context"
	"fmt"

	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

// SysGraph is a graph that is stored in memory.
type SysGraph struct {
	GraphPb     *sgpb.SysGraph
	Actions     map[int64]*sgpb.Action
	ResourceMap map[pbdigest.Digest]*sgpb.Resource
	Events      map[int64][]*anypb.Any
}

// Close closes the sysgraph.
func (sg *SysGraph) Close() error {
	return nil
}

// Proto returns the proto definition of the sysgraph.
func (sg *SysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	return sg.GraphPb
}

// Action returns the action with the given id.
func (sg *SysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	if a, ok := sg.Actions[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("action %d not found", id)
}

// Resource returns the resource with the given id.
func (sg *SysGraph) Resource(ctx context.Context, dg pbdigest.Digest) (*sgpb.Resource, error) {
	if r, ok := sg.ResourceMap[dg]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("resource for digest %s not found", dg)
}

// ActionIDs returns the ids of all actions in the sysgraph.
func (sg *SysGraph) ActionIDs(ctx context.Context) ([]int64, error) {
	return slices.Collect(maps.Keys(sg.Actions)), nil
}

// ResourceDigests returns the digests of all resources in the sysgraph.
func (sg *SysGraph) ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error) {
	return slices.Collect(maps.Keys(sg.ResourceMap)), nil
}

// Resources returns all resources in the sysgraph.
func (sg *SysGraph) Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error) {
	return maps.Clone(sg.ResourceMap), nil
}

// RawEventsForAction returns the raw events for the given action id.
func (sg *SysGraph) RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error) {
	return sg.Events[id], nil
}

// RawEvents returns the raw events from tetragon.
func (sg *SysGraph) RawEvents(ctx context.Context) ([]*anypb.Any, error) {
	var res []*anypb.Any
	for _, events := range sg.Events {
		res = append(res, events...)
	}
	return res, nil
}
