// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgtransform

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestOverrideProto(t *testing.T) {
	res := sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	builder := sgstorage.SysGraphBuilder{
		GraphPb: sgpb.SysGraph_builder{
			Id: proto.String("abcdefg"),
			Metadata: map[string]string{
				"foo": "bar",
				"baz": "qux",
			},
		}.Build(),
	}
	a0 := builder.Action("0")
	a0.StartTime = time.Unix(1, 1)
	a0.EndTime = time.Unix(10, 10)
	a0.AddInput(res, sgpb.ResourceInteraction_builder{
		IoInfo: sgpb.IOInfo_builder{
			BytesUsed: proto.Uint64(100),
		}.Build(),
	}.Build())
	original := builder.Build(context.Background())

	originalAction, err := original.Action(context.Background(), 1)
	if err != nil {
		t.Errorf("original.Action(0) returned unexpected error: %v", err)
	}
	if originalAction.GetSysGraphId() != "abcdefg" {
		t.Errorf("originalAction.GetSysGraphId() = %s, want %s", originalAction.GetSysGraphId(), "abcdefg")
	}
	overrideProto := sgpb.SysGraph_builder{
		Id: proto.String("somethingelse"),
		Metadata: map[string]string{
			"foo":     "bar",
			"another": "value",
		},
	}.Build()
	override := OverrideProto(original, overrideProto)

	overrideAction, err := override.Action(context.Background(), 1)
	if err != nil {
		t.Errorf("override.Action(0) returned unexpected error: %v", err)
	}

	if overrideAction.GetSysGraphId() != "somethingelse" {
		t.Errorf("overrideAction.GetSysGraphId() = %s, want %s", overrideAction.GetSysGraphId(), "somethingelse")
	}
	if diff := cmp.Diff(overrideProto, override.Proto(context.Background()), protocmp.Transform()); diff != "" {
		t.Errorf("override.Proto() had an unexpected diff, diff\n%s", diff)
	}
}
