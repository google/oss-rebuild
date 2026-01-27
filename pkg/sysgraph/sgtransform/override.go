// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"

	"google.golang.org/protobuf/proto"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// AddChildrenSysGraph is a sysgraph with additional children added to actions.
type AddChildrenSysGraph struct {
	SysGraph
	children map[int64][]*sgpb.ActionInteraction
	parents  map[int64]*sgpb.ActionInteraction
}

var _ SysGraph = (*AddChildrenSysGraph)(nil)

// WithProtoSysGraph wraps a SysGraph with a new metadata proto.
type WithProtoSysGraph struct {
	SysGraph
	proto *sgpb.SysGraph
}

var _ SysGraph = (*WithProtoSysGraph)(nil)

// OverrideProto overrides the SysGraph proto for the sysgraph.
func OverrideProto(sg SysGraph, proto *sgpb.SysGraph) *WithProtoSysGraph {
	return &WithProtoSysGraph{sg, proto}
}

// Proto returns the proto definition of the sysgraph.
func (w *WithProtoSysGraph) Proto(ctx context.Context) *sgpb.SysGraph {
	return w.proto
}

func (w *WithProtoSysGraph) Action(ctx context.Context, id int64) (*sgpb.Action, error) {
	a, err := w.SysGraph.Action(ctx, id)
	if err != nil {
		return nil, err
	}
	a = proto.Clone(a).(*sgpb.Action)
	a.SetSysGraphId(w.proto.GetId())
	return a, nil
}
