// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgir

import (
	"context"
	"os"
	"testing"

	"maps"
	"slices"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sgevpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestDiskFormat(t *testing.T) {
	testCases := []struct {
		name       string
		format     EventFileFormat
		wantFiles  []string
		wantEvents map[string][]*sgevpb.SysGraphEvent
	}{
		{
			name:   "jsonl",
			format: JSONL,
			wantFiles: []string{
				"action1.jsonl",
				"action2.jsonl",
			},
			wantEvents: map[string][]*sgevpb.SysGraphEvent{
				"action1": {
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgevpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action2"),
					}.Build(),
				},
			},
		},
		{
			name:   "proto",
			format: PBDelim,
			wantFiles: []string{
				"action1.pbdelim",
				"action2.pbdelim",
			},
			wantEvents: map[string][]*sgevpb.SysGraphEvent{
				"action1": {
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgevpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgevpb.SysGraphEvent_builder{
						ActionId: proto.String("action2"),
					}.Build(),
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			evDir := t.TempDir()
			df := &DiskFormat{
				BasePath: evDir,
				Format:   tc.format,
			}
			if _, err := df.WriteEvents(context.Background(),
				sgevpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
				}.Build(),
				sgevpb.SysGraphEvent_builder{
					ActionId: proto.String("action2"),
				}.Build(),
				sgevpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
					ChildEvent: sgevpb.ChildEvent_builder{
						ChildActionId: proto.String("action2"),
					}.Build(),
				}.Build(),
			); err != nil {
				t.Fatalf("WriteEvents failed: %v", err)
			}

			fileEntries, err := os.ReadDir(evDir)
			if err != nil {
				t.Fatalf("Failed to read directory: %v", err)
			}
			var gotFiles []string
			for _, f := range fileEntries {
				gotFiles = append(gotFiles, f.Name())
			}
			if diff := cmp.Diff(tc.wantFiles, gotFiles, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("WriteEvents wrote unexpected files (-want +got):\n%s", diff)
			}

			gotIDs, err := df.Actions(context.Background())
			if err != nil {
				t.Fatalf("DiskFormat.Actions() failed: %v", err)
			}
			if diff := cmp.Diff(
				slices.Collect(maps.Keys(tc.wantEvents)),
				gotIDs, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("Actions returned unexpected IDs (-want +got):\n%s", diff)
			}

			for id, wantEvents := range tc.wantEvents {
				gotEvents, err := df.Events(context.Background(), id)
				if err != nil {
					t.Fatalf("DiskFormat.Events(%q) failed: %v", id, err)
				}
				if diff := cmp.Diff(wantEvents, gotEvents, protocmp.Transform(), cmpopts.SortSlices(func(a, b *sgevpb.SysGraphEvent) bool { return a.String() < b.String() })); diff != "" {
					t.Errorf("Events(%q) returned unexpected events (-want +got):\n%s", id, diff)
				}
			}

		})
	}
}
