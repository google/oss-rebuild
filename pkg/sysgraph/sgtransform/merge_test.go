// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

func mustDigest(t *testing.T, m proto.Message) pbdigest.Digest {
	t.Helper()
	dg, err := pbdigest.NewFromMessage(m)
	if err != nil {
		t.Fatalf("Failed to create digest %q: %v", m, err)
	}
	return dg
}

func TestMerge(t *testing.T) {
	res1 := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	res2 := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file2"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	res3 := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file3"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()

	builder1 := sgstorage.SysGraphBuilder{
		GraphPb: sgpb.SysGraph_builder{
			Id: proto.String("abcdefg"),
			Metadata: map[string]string{
				"foo": "bar",
				"baz": "qux",
			},
		}.Build(),
	}

	b1a0 := builder1.Action("0")
	b1a0.StartTime = time.Unix(1, 1)
	b1a0.EndTime = time.Unix(2, 2)
	b1a0.AddInput(res1, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(1, 1)),
	}.Build())
	b1a0.AddOutput(res2, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(2, 2)),
	}.Build())

	b1a1 := builder1.Action("1")
	b1a1.StartTime = time.Unix(3, 3)
	b1a1.EndTime = time.Unix(4, 4)
	b1a1.AddInput(res3, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(3, 3)),
	}.Build())

	b1a2 := builder1.Action("1")
	b1a2.StartTime = time.Unix(3, 3)
	b1a2.EndTime = time.Unix(4, 4)
	b1a2.AddOutput(res2, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(3, 3)),
	}.Build())

	b1 := builder1.Build(context.Background())

	builder2 := sgstorage.SysGraphBuilder{
		GraphPb: sgpb.SysGraph_builder{
			Id: proto.String("defghij"),
			Metadata: map[string]string{
				"foo":       "bar",
				"something": "else",
			},
		}.Build(),
	}

	b2a0 := builder2.Action("0")
	b2a0.StartTime = time.Unix(10, 10)
	b2a0.EndTime = time.Unix(20, 20)
	b2a0.AddInput(res1, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(10, 10)),
	}.Build())
	b2a0.AddOutput(res2, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(20, 20)),
	}.Build())

	b2a1 := builder2.Action("1")
	b2a1.StartTime = time.Unix(30, 30)
	b2a1.EndTime = time.Unix(40, 40)
	b2a1.AddInput(res3, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(30, 30)),
	}.Build())

	b2a2 := builder2.Action("1")
	b2a2.StartTime = time.Unix(30, 30)
	b2a2.EndTime = time.Unix(40, 40)
	b2a2.AddOutput(res2, sgpb.ResourceInteraction_builder{
		Timestamp: tpb.New(time.Unix(30, 30)),
	}.Build())

	b2 := builder2.Build(context.Background())

	mergedSg, err := Merge(context.Background(), "new-id", b1, b2)
	if err != nil {
		t.Fatalf("Failed to merge sysgraphs: %v", err)
	}
	want := map[pbdigest.Digest]*sgpb.Resource{
		mustDigest(t, res1): res1,
		mustDigest(t, res2): res2,
		mustDigest(t, res3): res3,
	}
	got, err := mergedSg.Resources(context.Background())
	if err != nil {
		t.Fatalf("Failed to get resources: %v", err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("Resources() = %v, want %v, diff %s", got, want, diff)
	}
	wantActions := map[int64]*sgpb.Action{
		1: sgpb.Action_builder{
			Id:         proto.Int64(1),
			SysGraphId: proto.String("new-id"),
			StartTime:  &tpb.Timestamp{Seconds: 1, Nanos: 1},
			EndTime:    &tpb.Timestamp{Seconds: 2, Nanos: 2},
			Inputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res1).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 1, Nanos: 1},
					}.Build(),
				},
				}.Build(),
			},
			Outputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res2).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
					}.Build(),
				},
				}.Build(),
			},
		}.Build(),
		2: sgpb.Action_builder{
			Id:         proto.Int64(2),
			SysGraphId: proto.String("new-id"),
			StartTime:  &tpb.Timestamp{Seconds: 3, Nanos: 3},
			EndTime:    &tpb.Timestamp{Seconds: 4, Nanos: 4},
			Inputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res3).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
					}.Build(),
				},
				}.Build(),
			},
			Outputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res2).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
					}.Build(),
				},
				}.Build(),
			},
		}.Build(),
		3: sgpb.Action_builder{
			Id:         proto.Int64(3),
			SysGraphId: proto.String("new-id"),
			StartTime:  &tpb.Timestamp{Seconds: 10, Nanos: 10},
			EndTime:    &tpb.Timestamp{Seconds: 20, Nanos: 20},
			Inputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res1).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 10, Nanos: 10},
					}.Build(),
				},
				}.Build(),
			},
			Outputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res2).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 20, Nanos: 20},
					}.Build(),
				},
				}.Build(),
			},
		}.Build(),
		4: sgpb.Action_builder{
			Id:         proto.Int64(4),
			SysGraphId: proto.String("new-id"),
			StartTime:  &tpb.Timestamp{Seconds: 30, Nanos: 30},
			EndTime:    &tpb.Timestamp{Seconds: 40, Nanos: 40},
			Inputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res3).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 30, Nanos: 30},
					}.Build(),
				},
				}.Build(),
			},
			Outputs: map[string]*sgpb.ResourceInteractions{
				mustDigest(t, res2).String(): sgpb.ResourceInteractions_builder{Interactions: []*sgpb.ResourceInteraction{
					sgpb.ResourceInteraction_builder{
						Timestamp: &tpb.Timestamp{Seconds: 30, Nanos: 30},
					}.Build(),
				},
				}.Build(),
			},
		}.Build(),
	}
	gotActions, err := sgquery.MapAllActions(context.Background(), mergedSg,
		func(a *sgpb.Action) (int64, *sgpb.Action, error) {
			return a.GetId(), a, nil
		},
	)
	if err != nil {
		t.Fatalf("Failed to get actions: %v", err)
	}
	if diff := cmp.Diff(wantActions, gotActions, protocmp.Transform()); diff != "" {
		t.Errorf("MapAllActions() = %v, want %v, diff %s", gotActions, wantActions, diff)
	}
}
