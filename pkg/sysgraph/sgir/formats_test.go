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
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

func TestDiskFormat(t *testing.T) {
	testCases := []struct {
		name       string
		format     EventFileFormat
		wantFiles  []string
		wantEvents map[string][]*sgpb.SysGraphEvent
	}{
		{
			name:   "jsonl",
			format: JSONL,
			wantFiles: []string{
				"action1.jsonl",
				"action2.jsonl",
			},
			wantEvents: map[string][]*sgpb.SysGraphEvent{
				"action1": {
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgpb.SysGraphEvent_builder{
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
			wantEvents: map[string][]*sgpb.SysGraphEvent{
				"action1": {
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgpb.SysGraphEvent_builder{
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
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
				}.Build(),
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action2"),
				}.Build(),
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
					ChildEvent: sgpb.ChildEvent_builder{
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
				if diff := cmp.Diff(wantEvents, gotEvents, protocmp.Transform(), cmpopts.SortSlices(func(a, b *sgpb.SysGraphEvent) bool { return a.String() < b.String() })); diff != "" {
					t.Errorf("Events(%q) returned unexpected events (-want +got):\n%s", id, diff)
				}
			}

		})
	}
}

func TestBufferedDiskWriter(t *testing.T) {
	testCases := []struct {
		name       string
		format     EventFileFormat
		wantFiles  []string
		wantEvents map[string][]*sgpb.SysGraphEvent
	}{
		{
			name:   "jsonl",
			format: JSONL,
			wantFiles: []string{
				"action1.jsonl",
				"action2.jsonl",
			},
			wantEvents: map[string][]*sgpb.SysGraphEvent{
				"action1": {
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgpb.SysGraphEvent_builder{
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
			wantEvents: map[string][]*sgpb.SysGraphEvent{
				"action1": {
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
					}.Build(),
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action1"),
						ChildEvent: sgpb.ChildEvent_builder{
							ChildActionId: proto.String("action2"),
						}.Build(),
					}.Build(),
				},
				"action2": {
					sgpb.SysGraphEvent_builder{
						ActionId: proto.String("action2"),
					}.Build(),
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			evDir := t.TempDir()
			bw := NewBufferedDiskWriter(evDir, tc.format)

			if _, err := bw.WriteEvents(context.Background(),
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
				}.Build(),
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action2"),
				}.Build(),
				sgpb.SysGraphEvent_builder{
					ActionId: proto.String("action1"),
					ChildEvent: sgpb.ChildEvent_builder{
						ChildActionId: proto.String("action2"),
					}.Build(),
				}.Build(),
			); err != nil {
				t.Fatalf("WriteEvents failed: %v", err)
			}

			if err := bw.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}

			// Read back using DiskFormat to verify on-disk compatibility.
			df := &DiskFormat{BasePath: evDir, Format: tc.format}

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
				if diff := cmp.Diff(wantEvents, gotEvents, protocmp.Transform(), cmpopts.SortSlices(func(a, b *sgpb.SysGraphEvent) bool { return a.String() < b.String() })); diff != "" {
					t.Errorf("Events(%q) returned unexpected events (-want +got):\n%s", id, diff)
				}
			}
		})
	}
}

func TestBufferedDiskWriterMultipleCalls(t *testing.T) {
	evDir := t.TempDir()
	bw := NewBufferedDiskWriter(evDir, PBDelim)

	// First call: one event for action1.
	if _, err := bw.WriteEvents(context.Background(),
		sgpb.SysGraphEvent_builder{
			ActionId: proto.String("action1"),
		}.Build(),
	); err != nil {
		t.Fatalf("WriteEvents (call 1) failed: %v", err)
	}

	// Second call: another event for action1.
	if _, err := bw.WriteEvents(context.Background(),
		sgpb.SysGraphEvent_builder{
			ActionId: proto.String("action1"),
			ChildEvent: sgpb.ChildEvent_builder{
				ChildActionId: proto.String("action2"),
			}.Build(),
		}.Build(),
	); err != nil {
		t.Fatalf("WriteEvents (call 2) failed: %v", err)
	}

	if err := bw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read back and verify both events are present.
	df := &DiskFormat{BasePath: evDir, Format: PBDelim}
	gotEvents, err := df.Events(context.Background(), "action1")
	if err != nil {
		t.Fatalf("DiskFormat.Events(action1) failed: %v", err)
	}

	wantEvents := []*sgpb.SysGraphEvent{
		sgpb.SysGraphEvent_builder{
			ActionId: proto.String("action1"),
		}.Build(),
		sgpb.SysGraphEvent_builder{
			ActionId: proto.String("action1"),
			ChildEvent: sgpb.ChildEvent_builder{
				ChildActionId: proto.String("action2"),
			}.Build(),
		}.Build(),
	}
	if diff := cmp.Diff(wantEvents, gotEvents, protocmp.Transform(), cmpopts.SortSlices(func(a, b *sgpb.SysGraphEvent) bool { return a.String() < b.String() })); diff != "" {
		t.Errorf("Events(action1) returned unexpected events (-want +got):\n%s", diff)
	}
}

func TestBufferedDiskWriterCloseEmpty(t *testing.T) {
	evDir := t.TempDir()
	bw := NewBufferedDiskWriter(evDir, PBDelim)
	if err := bw.Close(); err != nil {
		t.Fatalf("Close on empty writer failed: %v", err)
	}
}
