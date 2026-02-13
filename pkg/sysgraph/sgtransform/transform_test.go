// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	anypb "google.golang.org/protobuf/types/known/anypb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

func mustAny(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	any, err := anypb.New(m)
	if err != nil {
		t.Fatalf("anypb.New(%q): %v", m, err)
	}
	return any
}

func TestLoadAfterDense(t *testing.T) {
	originalGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{1},
		}.Build(),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Children: map[int64]*sgpb.ActionInteraction{
					5: {},
				},
			}.Build(),
			5: sgpb.Action_builder{
				Id:             proto.Int64(5),
				ParentActionId: proto.Int64(1),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
			}.Build(),
		},
		Events: map[int64][]*anypb.Any{
			1: {mustAny(t, tpb.New(time.Unix(1, 1)))},
			5: {mustAny(t, tpb.New(time.Unix(5, 1)))},
		},
	}
	expectedGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{10},
		}.Build(),
		Actions: map[int64]*sgpb.Action{
			10: sgpb.Action_builder{
				Id: proto.Int64(10),
				Children: map[int64]*sgpb.ActionInteraction{
					11: sgpb.ActionInteraction_builder{}.Build(),
				},
			}.Build(),
			11: sgpb.Action_builder{
				Id:             proto.Int64(11),
				ParentActionId: proto.Int64(10),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
			}.Build(),
		},
		Events: map[int64][]*anypb.Any{
			10: {mustAny(t, tpb.New(time.Unix(1, 1)))},
			11: {mustAny(t, tpb.New(time.Unix(5, 1)))},
		},
	}
	dense, _, err := Dense(context.Background(), originalGraph, 10)
	if err != nil {
		t.Fatalf("Dense returned unexpected error: %v", err)
	}
	loaded, err := Load(context.Background(), dense)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if diff := cmp.Diff(loaded, expectedGraph, protocmp.Transform()); diff != "" {
		t.Errorf("Load returned unexpected sysgraph, diff\n%s", diff)
	}
}
