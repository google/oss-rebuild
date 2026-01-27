// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgquery

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestProcessTree(t *testing.T) {
	tests := []struct {
		name     string
		wantTree *MinimalProcessTree
		sg       ActionProvider
	}{
		{
			name: "flat",
			sg: &inmemory.SysGraph{
				Actions: map[int64]*sgpb.Action{
					1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
					2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
					3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
					4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
					5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
				},
			},
			wantTree: &MinimalProcessTree{
				Children: map[int64]*MinimalProcessTree{
					1: {
						ActionID: 1,
						Children: map[int64]*MinimalProcessTree{},
					},
					2: {
						ActionID: 2,
						Children: map[int64]*MinimalProcessTree{},
					},
					3: {
						ActionID: 3,
						Children: map[int64]*MinimalProcessTree{},
					},
					4: {
						ActionID: 4,
						Children: map[int64]*MinimalProcessTree{},
					},
					5: {
						ActionID: 5,
						Children: map[int64]*MinimalProcessTree{},
					},
				},
			},
		},
		{
			name: "tree",
			sg: &inmemory.SysGraph{
				Actions: map[int64]*sgpb.Action{
					1: sgpb.Action_builder{
						Id: proto.Int64(1),
						Children: map[int64]*sgpb.ActionInteraction{
							2: sgpb.ActionInteraction_builder{}.Build(),
							5: sgpb.ActionInteraction_builder{}.Build(),
						},
					}.Build(),
					2: sgpb.Action_builder{
						Id:             proto.Int64(2),
						ParentActionId: proto.Int64(1),
						Children: map[int64]*sgpb.ActionInteraction{
							4: sgpb.ActionInteraction_builder{}.Build(),
						},
					}.Build(),
					3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
					4: sgpb.Action_builder{Id: proto.Int64(4), ParentActionId: proto.Int64(2)}.Build(),
					5: sgpb.Action_builder{Id: proto.Int64(5), ParentActionId: proto.Int64(1)}.Build(),
				},
			},
			wantTree: &MinimalProcessTree{
				Children: map[int64]*MinimalProcessTree{
					1: {
						ActionID: 1,
						Children: map[int64]*MinimalProcessTree{
							2: {
								ActionID: 2,
								Children: map[int64]*MinimalProcessTree{
									4: {
										ActionID: 4,
										Children: map[int64]*MinimalProcessTree{},
									},
								},
							},
							5: {
								ActionID: 5,
								Children: map[int64]*MinimalProcessTree{},
							},
						},
					},
					3: {
						ActionID: 3,
						Children: map[int64]*MinimalProcessTree{},
					},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTree, err := ProcessTree(context.Background(), tc.sg)
			if err != nil {
				t.Fatalf("Failed to get process tree from sysgraph: %v", err)
			}
			if diff := cmp.Diff(tc.wantTree, gotTree, protocmp.Transform()); diff != "" {
				t.Errorf("Process tree diff (-want +got):\n%s", diff)
			}
		})
	}
}
