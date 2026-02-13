// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"fmt"

	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
)

// MergedSysGraph is a sysgraph that is the union of multiple sysgraphs.
// Action ids are mapped to successive ranges.
type MergedSysGraph struct {
	sgid string
	sgs  []*DenseSysGraph
	ends []int64
	aids []int64
	rdgs map[pbdigest.Digest]int
}

var _ SysGraph = (*MergedSysGraph)(nil)

// Close closes the merged sysgraph.
func (msg *MergedSysGraph) Close() error {
	for _, sg := range msg.sgs {
		if err := sg.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Proto returns the proto definition of the sysgraph.
func (msg *MergedSysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	pb := proto.Clone(msg.sgs[0].Proto(ctx)).(*sgpb.SysGraph)
	for _, sg := range msg.sgs[1:] {
		proto.Merge(pb, sg.Proto(ctx))
	}
	pb.SetId(msg.sgid)
	return pb
}

func (msg *MergedSysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	for i, end := range msg.ends {
		if id < end {
			a, err := msg.sgs[i].Action(ctx, id)
			if err != nil {
				return nil, err
			}
			a = proto.Clone(a).(*sgpb.Action)
			a.SetSysGraphId(msg.sgid)
			return a, nil
		}
	}
	return nil, fmt.Errorf("merge: action %d not found", id)
}

func (msg *MergedSysGraph) Resource(ctx context.Context, dg pbdigest.Digest) (*sgpb.Resource, error) {
	i, ok := msg.rdgs[dg]
	if !ok {
		return nil, fmt.Errorf("resource %s not found", dg)
	}
	return msg.sgs[i].Resource(ctx, dg)
}

// ResourceDigests returns the digests of all resources in the sysgraph.
func (msg *MergedSysGraph) ResourceDigests(ctx context.Context) ([]pbdigest.Digest, error) {
	return slices.Collect(maps.Keys(msg.rdgs)), nil
}

// Resources returns the all resources in the sysgraph.
func (msg *MergedSysGraph) Resources(ctx context.Context) (map[pbdigest.Digest]*sgpb.Resource, error) {
	m := make(map[pbdigest.Digest]*sgpb.Resource, len(msg.rdgs))
	for dg, i := range msg.rdgs {
		r, err := msg.sgs[i].Resource(ctx, dg)
		if err != nil {
			return nil, err
		}
		m[dg] = r
	}
	return m, nil
}

// ActionIDs returns the action ids in the merged sysgraph.
func (msg *MergedSysGraph) ActionIDs(ctx context.Context) ([]int64, error) {
	return msg.aids, nil
}

// MergedActionID returns the action id of the given action id in its original sysgraph.
func (msg *MergedSysGraph) MergedActionID(sgIdx int, originalAid int64) int64 {
	return msg.sgs[sgIdx].DenseActionID(originalAid)
}

// RawEvents returns all raw events from tetragon. This is not implemented for merged sysgraphs.
func (msg *MergedSysGraph) RawEvents(ctx context.Context) ([]*anypb.Any, error) {
	return nil, nil
}

// RawEventsForAction returns the raw events for the given action from tetragon.
// This is not implemented for merged sysgraphs.
func (msg *MergedSysGraph) RawEventsForAction(ctx context.Context, id int64) ([]*anypb.Any, error) {
	return nil, nil
}

// Merge merges the given sysgraphs into a single sysgraph.
// The merged sysgraph id is set to newID.
// Action ids are mapped to successive ranges.
func Merge(ctx context.Context, newID string, sgs ...SysGraph) (*MergedSysGraph, error) {
	densesgs := make([]*DenseSysGraph, 0, len(sgs))
	start := int64(1)
	rdgs := make(map[pbdigest.Digest]int)
	aids := []int64{}
	ends := []int64{}
	for i, sg := range sgs {
		densesg, size, err := Dense(ctx, sg, start)
		if err != nil {
			return nil, err
		}
		densesgs = append(densesgs, densesg)
		dgs, err := sg.Resources(ctx)
		if err != nil {
			return nil, err
		}
		for dg := range dgs {
			rdgs[dg] = i
		}
		for k := int64(0); k < size; k++ {
			aids = append(aids, k+start)
		}
		start += size
		ends = append(ends, start)
	}
	return &MergedSysGraph{
		sgs:  densesgs,
		sgid: newID,
		aids: aids,
		rdgs: rdgs,
		ends: ends,
	}, nil
}
