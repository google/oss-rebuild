// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

var (
	file1 = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path: proto.String("path/to/file1"),
			Type: sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	file2 = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path: proto.String("path/to/file2"),
			Type: sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
)

func resourceMap(t *testing.T, resources ...*sgpb.Resource) map[pbdigest.Digest]*sgpb.Resource {
	t.Helper()
	res := make(map[pbdigest.Digest]*sgpb.Resource, len(resources))
	for _, r := range resources {
		dg, err := pbdigest.NewFromMessage(r)
		if err != nil {
			t.Fatalf("Failed to create digest %q: %v", r, err)
		}
		res[dg] = r
	}
	return res
}

func TestSubgraphForRoots(t *testing.T) {
	originalGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{1},
		}.Build(),
		ResourceMap: resourceMap(t, file1, file2),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Children: map[int64]*sgpb.ActionInteraction{
					2: {},
					4: {},
				},
				Inputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file1).String(): {},
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id:             proto.Int64(2),
				ParentActionId: proto.Int64(1),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					3: {},
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id:             proto.Int64(3),
				ParentActionId: proto.Int64(2),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					5: {},
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id:             proto.Int64(4),
				ParentActionId: proto.Int64(1),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
			}.Build(),
			5: sgpb.Action_builder{
				Id:             proto.Int64(5),
				ParentActionId: proto.Int64(3),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Outputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file2).String(): {},
				},
			}.Build(),
		},
	}
	expectedGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{2, 4},
		}.Build(),
		ResourceMap: resourceMap(t, file2),
		Actions: map[int64]*sgpb.Action{
			2: sgpb.Action_builder{
				Id: proto.Int64(2),
				Children: map[int64]*sgpb.ActionInteraction{
					3: {},
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id:             proto.Int64(3),
				ParentActionId: proto.Int64(2),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					5: {},
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id: proto.Int64(4),
			}.Build(),
			5: sgpb.Action_builder{
				Id:             proto.Int64(5),
				ParentActionId: proto.Int64(3),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Outputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file2).String(): {},
				},
			}.Build(),
		},
	}
	roots := []int64{2, 3, 4}
	gotGraph, err := SubgraphForRoots(context.Background(), originalGraph, roots)
	if err != nil {
		t.Fatalf("SubgraphForRoots returned unexpected error: %v", err)
	}
	loadedGraph, err := Load(context.Background(), gotGraph)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if diff := cmp.Diff(loadedGraph, expectedGraph, protocmp.Transform(), cmpopts.SortSlices(func(a, b int64) bool { return a < b }), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("SubgraphForRoots returned unexpected sysgraph, diff\n%s", diff)
	}
}

func TestFilterForRoot(t *testing.T) {
	originalGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{1},
		}.Build(),
		ResourceMap: resourceMap(t, file1, file2),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Children: map[int64]*sgpb.ActionInteraction{
					2: {},
					4: {},
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin1"},
				}.Build(),
				Inputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file1).String(): {},
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id:             proto.Int64(2),
				ParentActionId: proto.Int64(1),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Metadata: map[string]string{
					"otherkey": "othervalue",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin2"},
				}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					3: {},
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id:             proto.Int64(3),
				ParentActionId: proto.Int64(2),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Metadata: map[string]string{
					"somekey": "value",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin3"},
				}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					5: {},
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id:             proto.Int64(4),
				ParentActionId: proto.Int64(1),
				Metadata: map[string]string{
					"somekey": "value",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin4"},
				}.Build(),
				Parent: sgpb.ActionInteraction_builder{}.Build(),
			}.Build(),
			5: sgpb.Action_builder{
				Id:             proto.Int64(5),
				ParentActionId: proto.Int64(3),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Outputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file2).String(): {},
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin5"},
				}.Build(),
			}.Build(),
		},
	}

	testCases := []struct {
		name   string
		filter func(ctx context.Context, a *sgpb.Action) bool
		want   *inmemory.SysGraph
	}{
		{
			name: "picks_first_bfs",
			filter: func(ctx context.Context, a *sgpb.Action) bool {
				return a.GetMetadata()["somekey"] == "value"
			},
			want: &inmemory.SysGraph{
				GraphPb: sgpb.SysGraph_builder{
					EntryPointActionIds: []int64{1},
				}.Build(),
				Actions: map[int64]*sgpb.Action{
					1: sgpb.Action_builder{
						Id: proto.Int64(1),
						Metadata: map[string]string{
							"somekey": "value",
						},
						ExecInfo: sgpb.ExecInfo_builder{
							Argv: []string{"bin4"},
						}.Build(),
					}.Build(),
				},
			},
		}, {
			name: "original_root_noop",
			filter: func(ctx context.Context, a *sgpb.Action) bool {
				return a.GetExecInfo().GetArgv()[0] == "bin1"
			},
			want: originalGraph,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotGraph, err := FilterForRoot(context.Background(), originalGraph, tc.filter)
			if err != nil {
				t.Fatalf("FilterForRoot returned unexpected error: %v", err)
			}

			loadedGraph, err := Load(context.Background(), gotGraph)
			if err != nil {
				t.Fatalf("Load returned unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.want, loadedGraph, protocmp.Transform(), cmpopts.SortSlices(func(a, b int64) bool { return a < b }), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("FilterForRoot returned unexpected sysgraph, diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFilterForRoot_NoMatch(t *testing.T) {
	originalGraph := &inmemory.SysGraph{
		GraphPb: sgpb.SysGraph_builder{
			EntryPointActionIds: []int64{1},
		}.Build(),
		ResourceMap: resourceMap(t, file1, file2),
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Children: map[int64]*sgpb.ActionInteraction{
					2: {},
					4: {},
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin1"},
				}.Build(),
				Inputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file1).String(): {},
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id:             proto.Int64(2),
				ParentActionId: proto.Int64(1),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Metadata: map[string]string{
					"otherkey": "othervalue",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin2"},
				}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					3: {},
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id:             proto.Int64(3),
				ParentActionId: proto.Int64(2),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Metadata: map[string]string{
					"somekey": "value",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin3"},
				}.Build(),
				Children: map[int64]*sgpb.ActionInteraction{
					5: {},
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id:             proto.Int64(4),
				ParentActionId: proto.Int64(1),
				Metadata: map[string]string{
					"somekey": "value",
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin4"},
				}.Build(),
				Parent: sgpb.ActionInteraction_builder{}.Build(),
			}.Build(),
			5: sgpb.Action_builder{
				Id:             proto.Int64(5),
				ParentActionId: proto.Int64(3),
				Parent:         sgpb.ActionInteraction_builder{}.Build(),
				Outputs: map[string]*sgpb.ResourceInteractions{
					mustDigest(t, file2).String(): {},
				},
				ExecInfo: sgpb.ExecInfo_builder{
					Argv: []string{"bin5"},
				}.Build(),
			}.Build(),
		},
	}

	noMatchFilter := func(ctx context.Context, a *sgpb.Action) bool {
		return a.GetExecInfo().GetArgv()[0] == "bin6"
	}
	_, err := FilterForRoot(context.Background(), originalGraph, noMatchFilter)

	if err == nil {
		t.Errorf("FilterForRoot did not return an error, but expected one")
	}

	if !errors.Is(err, sgquery.ErrNoActionFound) {
		t.Errorf("FilterForRoot returned unexpected error: %v", err)
	}
}
