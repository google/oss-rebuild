// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tetragon

import (
	"sort"
	"testing"
	"time"

	"slices"

	tetragonpb "github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sgevpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgir"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestConvertProcessExec(t *testing.T) {
	ts := tpb.New(time.Unix(100, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExec{
				ProcessExec: &tetragonpb.ProcessExec{
					Process: &tetragonpb.Process{
						ExecId:       "child-exec-id",
						Binary:       "/usr/bin/gcc",
						Arguments:    "-o main main.c",
						Cwd:          "/src",
						StartTime:    ts,
						ParentExecId: "parent-exec-id",
						Pid:          &wrapperspb.UInt32Value{Value: 42},
					},
					Parent: &tetragonpb.Process{
						ExecId:    "parent-exec-id",
						Binary:    "/bin/sh",
						StartTime: ts,
						Pid:       &wrapperspb.UInt32Value{Value: 1},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	conv.StoreRawEvents = true
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	// Check child action events: ExecEvent, StartEvent, MetadataEvents, (ChildEvent goes to parent)
	childEvents, ok := mem.EventMap["child-exec-id"]
	if !ok {
		t.Fatal("expected events for child-exec-id")
	}

	var hasStart, hasExec bool
	var metadataKeys []string
	for _, e := range childEvents.Events {
		if e.HasStartEvent() {
			hasStart = true
		}
		if e.HasExecEvent() {
			hasExec = true
		}
		if e.HasMetadataEvent() {
			metadataKeys = append(metadataKeys, e.GetMetadataEvent().GetKey())
		}
	}
	if !hasStart {
		t.Error("expected StartEvent for child")
	}
	if !hasExec {
		t.Error("expected ExecEvent for child")
	}
	// Should have exec_id and psns metadata
	if !slices.Contains(metadataKeys, "exec_id") {
		t.Error("expected exec_id metadata on child")
	}
	if !slices.Contains(metadataKeys, "psns") {
		t.Error("expected psns metadata on child")
	}

	// Check ExecEvent details
	for _, e := range childEvents.Events {
		if e.HasExecEvent() {
			wantExec := sgevpb.ExecEvent_builder{
				Executable: sgevpb.Resource_builder{
					Type: sgevpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
					FileInfo: sgevpb.FileInfo_builder{
						Path: proto.String("/usr/bin/gcc"),
						Type: sgevpb.FileType_FILE_TYPE_REGULAR.Enum(),
					}.Build(),
				}.Build(),
				ExecInfo: sgevpb.ExecInfo_builder{
					Argv:             []string{"/usr/bin/gcc", "-o", "main", "main.c"},
					WorkingDirectory: proto.String("/src"),
					Pid:              proto.Int64(42),
					Tid:              proto.Int64(0),
				}.Build(),
			}.Build()
			if diff := cmp.Diff(wantExec, e.GetExecEvent(), protocmp.Transform()); diff != "" {
				t.Errorf("ExecEvent mismatch (-want +got):\n%s", diff)
			}
		}
	}

	// Check parent action events: should have process-level events AND ChildEvent
	parentEvents, ok := mem.EventMap["parent-exec-id"]
	if !ok {
		t.Fatal("expected events for parent-exec-id")
	}
	foundChild := false
	foundParentExec := false
	for _, e := range parentEvents.Events {
		if e.HasChildEvent() && e.GetChildEvent().GetChildActionId() == "child-exec-id" {
			foundChild = true
		}
		if e.HasExecEvent() {
			foundParentExec = true
		}
	}
	if !foundChild {
		t.Error("expected ChildEvent on parent for child-exec-id")
	}
	if !foundParentExec {
		t.Error("expected ExecEvent on parent (from parent process accounting)")
	}

	// Verify raw events written for child
	if len(childEvents.RawEvents) != 1 {
		t.Errorf("expected 1 raw event for child, got %d", len(childEvents.RawEvents))
	}
}

func TestConvertProcessExecDedup(t *testing.T) {
	ts := tpb.New(time.Unix(100, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExec{
				ProcessExec: &tetragonpb.ProcessExec{
					Process: &tetragonpb.Process{
						ExecId:    "proc1",
						Binary:    "/bin/sh",
						StartTime: ts,
					},
				},
			},
			Time: ts,
		},
		// Second event referencing proc1 as parent should not emit proc1 events again
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:    "proc1",
						Binary:    "/bin/sh",
						StartTime: ts,
					},
					FunctionName: "security_file_permission",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_FileArg{
							FileArg: &tetragonpb.KprobeFile{Mount: "/", Path: "src/file.txt"},
						}},
						{Arg: &tetragonpb.KprobeArgument_IntArg{IntArg: 0x04}},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	// Count StartEvents for proc1 - should be exactly 1 (deduped)
	proc1Events := mem.EventMap["proc1"]
	startCount := 0
	for _, e := range proc1Events.Events {
		if e.HasStartEvent() {
			startCount++
		}
	}
	if startCount != 1 {
		t.Errorf("expected 1 StartEvent for proc1 (dedup), got %d", startCount)
	}
}

func TestConvertProcessExit(t *testing.T) {
	ts := tpb.New(time.Unix(200, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExit{
				ProcessExit: &tetragonpb.ProcessExit{
					Process: &tetragonpb.Process{
						ExecId:    "exit-exec-id",
						Binary:    "/bin/sh",
						StartTime: ts,
					},
					Signal: "SIGTERM",
					Status: 1,
					Time:   ts,
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	actionEvents, ok := mem.EventMap["exit-exec-id"]
	if !ok {
		t.Fatal("expected events for exit-exec-id")
	}
	var hasEnd bool
	for _, e := range actionEvents.Events {
		if e.HasEndEvent() {
			hasEnd = true
			endEvent := e.GetEndEvent()
			if endEvent.GetStatus() != 1 {
				t.Errorf("EndEvent.Status = %d, want 1", endEvent.GetStatus())
			}
			if endEvent.GetSignal() != "SIGTERM" {
				t.Errorf("EndEvent.Signal = %q, want %q", endEvent.GetSignal(), "SIGTERM")
			}
		}
	}
	if !hasEnd {
		t.Error("expected EndEvent")
	}
}

func TestConvertSecurityFilePermission(t *testing.T) {
	ts := tpb.New(time.Unix(300, 0))
	tests := []struct {
		name     string
		perm     int32
		wantType sgevpb.ResourceEvent_EventType
	}{
		{"read", 0x04, sgevpb.ResourceEvent_EVENT_TYPE_INPUT},
		{"write", 0x02, sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT},
		{"read_write", 0x06, sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := []*tetragonpb.GetEventsResponse{
				{
					Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
						ProcessKprobe: &tetragonpb.ProcessKprobe{
							Process: &tetragonpb.Process{
								ExecId:    "kprobe-exec-id",
								Binary:    "/usr/bin/cc",
								StartTime: ts,
							},
							FunctionName: "security_file_permission",
							Args: []*tetragonpb.KprobeArgument{
								{Arg: &tetragonpb.KprobeArgument_FileArg{
									FileArg: &tetragonpb.KprobeFile{
										Mount: "/",
										Path:  "src/main.c",
									},
								}},
								{Arg: &tetragonpb.KprobeArgument_IntArg{
									IntArg: tc.perm,
								}},
							},
						},
					},
					Time: ts,
				},
			}

			mem := &sgir.InMemoryFormat{}
			conv := NewConverter()
			if err := conv.Convert(t.Context(), events, mem); err != nil {
				t.Fatalf("Convert() error: %v", err)
			}

			actionEvents, ok := mem.EventMap["kprobe-exec-id"]
			if !ok {
				t.Fatal("expected events for kprobe-exec-id")
			}
			var foundResource bool
			for _, e := range actionEvents.Events {
				if e.HasResourceEvent() {
					foundResource = true
					re := e.GetResourceEvent()
					if re.GetEventType() != tc.wantType {
						t.Errorf("EventType = %v, want %v", re.GetEventType(), tc.wantType)
					}
					if re.GetResource().GetFileInfo().GetPath() != "/src/main.c" {
						t.Errorf("Path = %q, want %q", re.GetResource().GetFileInfo().GetPath(), "/src/main.c")
					}
				}
			}
			if !foundResource {
				t.Error("expected ResourceEvent")
			}
		})
	}
}

func TestConvertSecurityMmapFile(t *testing.T) {
	ts := tpb.New(time.Unix(400, 0))
	tests := []struct {
		name     string
		prot     uint32
		wantType sgevpb.ResourceEvent_EventType
	}{
		{"read", 0x01, sgevpb.ResourceEvent_EVENT_TYPE_INPUT},
		{"write", 0x02, sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT},
		{"exec", 0x04, sgevpb.ResourceEvent_EVENT_TYPE_INPUT},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := []*tetragonpb.GetEventsResponse{
				{
					Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
						ProcessKprobe: &tetragonpb.ProcessKprobe{
							Process: &tetragonpb.Process{
								ExecId:    "mmap-exec-id",
								Binary:    "/usr/bin/ld",
								StartTime: ts,
							},
							FunctionName: "security_mmap_file",
							Args: []*tetragonpb.KprobeArgument{
								{Arg: &tetragonpb.KprobeArgument_FileArg{
									FileArg: &tetragonpb.KprobeFile{
										Mount: "/",
										Path:  "lib/libc.so.6",
									},
								}},
								{Arg: &tetragonpb.KprobeArgument_UintArg{
									UintArg: tc.prot,
								}},
							},
						},
					},
					Time: ts,
				},
			}

			mem := &sgir.InMemoryFormat{}
			conv := NewConverter()
			if err := conv.Convert(t.Context(), events, mem); err != nil {
				t.Fatalf("Convert() error: %v", err)
			}

			actionEvents := mem.EventMap["mmap-exec-id"]
			for _, e := range actionEvents.Events {
				if e.HasResourceEvent() {
					re := e.GetResourceEvent()
					if re.GetEventType() != tc.wantType {
						t.Errorf("EventType = %v, want %v", re.GetEventType(), tc.wantType)
					}
					return
				}
			}
			t.Error("expected ResourceEvent")
		})
	}
}

func TestConvertSecurityPathTruncate(t *testing.T) {
	ts := tpb.New(time.Unix(500, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:    "truncate-exec-id",
						Binary:    "/usr/bin/cp",
						StartTime: ts,
					},
					FunctionName: "security_path_truncate",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{
								Mount: "/",
								Path:  "tmp/output.log",
							},
						}},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	actionEvents := mem.EventMap["truncate-exec-id"]
	for _, e := range actionEvents.Events {
		if e.HasResourceEvent() {
			re := e.GetResourceEvent()
			if re.GetEventType() != sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT {
				t.Errorf("EventType = %v, want OUTPUT", re.GetEventType())
			}
			if re.GetResource().GetFileInfo().GetPath() != "/tmp/output.log" {
				t.Errorf("Path = %q, want %q", re.GetResource().GetFileInfo().GetPath(), "/tmp/output.log")
			}
			return
		}
	}
	t.Error("expected ResourceEvent")
}

func TestConvertSecurityPathRename(t *testing.T) {
	ts := tpb.New(time.Unix(510, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:    "rename-exec-id",
						Binary:    "/bin/mv",
						StartTime: ts,
					},
					FunctionName: "security_path_rename",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Mount: "/", Path: "src"},
						}},
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Path: "old.txt"},
						}},
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Mount: "/", Path: "dst"},
						}},
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Path: "new.txt"},
						}},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	actionEvents := mem.EventMap["rename-exec-id"]
	var paths []string
	for _, e := range actionEvents.Events {
		if e.HasResourceEvent() {
			re := e.GetResourceEvent()
			if re.GetEventType() != sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT {
				t.Errorf("EventType = %v, want OUTPUT", re.GetEventType())
			}
			paths = append(paths, re.GetResource().GetFileInfo().GetPath())
		}
	}
	wantPaths := []string{"/src/old.txt", "/dst/new.txt"}
	sort.Strings(paths)
	sort.Strings(wantPaths)
	if diff := cmp.Diff(wantPaths, paths); diff != "" {
		t.Errorf("paths mismatch (-want +got):\n%s", diff)
	}
}

func TestConvertSecurityPathUnlink(t *testing.T) {
	ts := tpb.New(time.Unix(520, 0))
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:    "unlink-exec-id",
						Binary:    "/bin/rm",
						StartTime: ts,
					},
					FunctionName: "security_path_unlink",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Mount: "/", Path: "tmp"},
						}},
						{Arg: &tetragonpb.KprobeArgument_PathArg{
							PathArg: &tetragonpb.KprobePath{Path: "garbage.o"},
						}},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	actionEvents := mem.EventMap["unlink-exec-id"]
	for _, e := range actionEvents.Events {
		if e.HasResourceEvent() {
			re := e.GetResourceEvent()
			if re.GetEventType() != sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT {
				t.Errorf("EventType = %v, want OUTPUT", re.GetEventType())
			}
			if re.GetResource().GetFileInfo().GetPath() != "/tmp/garbage.o" {
				t.Errorf("Path = %q, want %q", re.GetResource().GetFileInfo().GetPath(), "/tmp/garbage.o")
			}
			return
		}
	}
	t.Error("expected ResourceEvent")
}

func TestConvertParentProcessAccounting(t *testing.T) {
	ts := tpb.New(time.Unix(100, 0))

	// A kprobe event with a parent. The parent should get process events even
	// though we never saw a ProcessExec for it.
	events := []*tetragonpb.GetEventsResponse{
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:       "child",
						Binary:       "/usr/bin/make",
						StartTime:    ts,
						ParentExecId: "parent",
					},
					Parent: &tetragonpb.Process{
						ExecId:    "parent",
						Binary:    "/bin/sh",
						StartTime: ts,
						Docker:    "abc123",
					},
					FunctionName: "security_file_permission",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_FileArg{
							FileArg: &tetragonpb.KprobeFile{Mount: "/", Path: "src/Makefile"},
						}},
						{Arg: &tetragonpb.KprobeArgument_IntArg{IntArg: 0x04}},
					},
				},
			},
			Time: ts,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	// Parent should have process-level events even though no ProcessExec was sent for it
	parentEvents, ok := mem.EventMap["parent"]
	if !ok {
		t.Fatal("expected events for parent (from parent process accounting)")
	}
	var hasParentStart, hasParentExec, hasDockerMeta bool
	for _, e := range parentEvents.Events {
		if e.HasStartEvent() {
			hasParentStart = true
		}
		if e.HasExecEvent() {
			hasParentExec = true
		}
		if e.HasMetadataEvent() && e.GetMetadataEvent().GetKey() == "docker" {
			hasDockerMeta = true
			if e.GetMetadataEvent().GetValue() != "abc123" {
				t.Errorf("docker metadata = %q, want %q", e.GetMetadataEvent().GetValue(), "abc123")
			}
		}
	}
	if !hasParentStart {
		t.Error("expected StartEvent on parent")
	}
	if !hasParentExec {
		t.Error("expected ExecEvent on parent")
	}
	if !hasDockerMeta {
		t.Error("expected docker metadata on parent")
	}

	// Parent should also have a ChildEvent for child
	var hasChildEvent bool
	for _, e := range parentEvents.Events {
		if e.HasChildEvent() && e.GetChildEvent().GetChildActionId() == "child" {
			hasChildEvent = true
		}
	}
	if !hasChildEvent {
		t.Error("expected ChildEvent on parent for child")
	}
}

func TestConvertEndToEnd(t *testing.T) {
	ts1 := tpb.New(time.Unix(1, 0))
	ts2 := tpb.New(time.Unix(2, 0))
	ts3 := tpb.New(time.Unix(3, 0))
	ts4 := tpb.New(time.Unix(4, 0))

	events := []*tetragonpb.GetEventsResponse{
		// Parent process starts
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExec{
				ProcessExec: &tetragonpb.ProcessExec{
					Process: &tetragonpb.Process{
						ExecId:    "parent",
						Binary:    "/bin/sh",
						Arguments: "-c make",
						Cwd:       "/src",
						StartTime: ts1,
					},
				},
			},
			Time: ts1,
		},
		// Child process starts
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExec{
				ProcessExec: &tetragonpb.ProcessExec{
					Process: &tetragonpb.Process{
						ExecId:       "child",
						Binary:       "/usr/bin/make",
						Cwd:          "/src",
						StartTime:    ts2,
						ParentExecId: "parent",
					},
					Parent: &tetragonpb.Process{
						ExecId:    "parent",
						Binary:    "/bin/sh",
						StartTime: ts1,
					},
				},
			},
			Time: ts2,
		},
		// Child reads a file
		{
			Event: &tetragonpb.GetEventsResponse_ProcessKprobe{
				ProcessKprobe: &tetragonpb.ProcessKprobe{
					Process: &tetragonpb.Process{
						ExecId:    "child",
						Binary:    "/usr/bin/make",
						StartTime: ts2,
					},
					FunctionName: "security_file_permission",
					Args: []*tetragonpb.KprobeArgument{
						{Arg: &tetragonpb.KprobeArgument_FileArg{
							FileArg: &tetragonpb.KprobeFile{Mount: "/", Path: "src/Makefile"},
						}},
						{Arg: &tetragonpb.KprobeArgument_IntArg{IntArg: 0x04}}, // MAY_READ
					},
				},
			},
			Time: ts3,
		},
		// Child exits
		{
			Event: &tetragonpb.GetEventsResponse_ProcessExit{
				ProcessExit: &tetragonpb.ProcessExit{
					Process: &tetragonpb.Process{
						ExecId:    "child",
						Binary:    "/usr/bin/make",
						StartTime: ts2,
					},
					Status: 0,
					Signal: "",
					Time:   ts4,
				},
			},
			Time: ts4,
		},
	}

	mem := &sgir.InMemoryFormat{}
	conv := NewConverter()
	conv.StoreRawEvents = true
	if err := conv.Convert(t.Context(), events, mem); err != nil {
		t.Fatalf("Convert() error: %v", err)
	}

	// Verify we have the right actions
	wantActions := []string{"parent", "child"}
	actions, err := mem.Actions(t.Context())
	if err != nil {
		t.Fatalf("Actions() error: %v", err)
	}
	if diff := cmp.Diff(wantActions, actions, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("Actions mismatch (-want +got):\n%s", diff)
	}

	// Verify parent has ChildEvent
	parentEvents := mem.EventMap["parent"]
	foundChild := false
	for _, e := range parentEvents.Events {
		if e.HasChildEvent() && e.GetChildEvent().GetChildActionId() == "child" {
			foundChild = true
		}
	}
	if !foundChild {
		t.Error("expected ChildEvent on parent for child")
	}

	// Verify child has StartEvent, ExecEvent, ResourceEvent (read), EndEvent
	childEvents := mem.EventMap["child"]
	var hasStart, hasExec, hasResource, hasEnd bool
	for _, e := range childEvents.Events {
		if e.HasStartEvent() {
			hasStart = true
		}
		if e.HasExecEvent() {
			hasExec = true
		}
		if e.HasResourceEvent() {
			hasResource = true
			re := e.GetResourceEvent()
			if re.GetEventType() != sgevpb.ResourceEvent_EVENT_TYPE_INPUT {
				t.Errorf("ResourceEvent type = %v, want INPUT", re.GetEventType())
			}
		}
		if e.HasEndEvent() {
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("child missing StartEvent")
	}
	if !hasExec {
		t.Error("child missing ExecEvent")
	}
	if !hasResource {
		t.Error("child missing ResourceEvent")
	}
	if !hasEnd {
		t.Error("child missing EndEvent")
	}

	// Build the sysgraph from the IR to verify end-to-end
	builder := &sgir.Builder{
		ConcurrencyLimit: 1,
		StoreRawEvents:   true,
	}
	sgDir := t.TempDir()
	if err := builder.ToSysGraph(t.Context(), "test-graph", mem, sgDir); err != nil {
		t.Fatalf("ToSysGraph() error: %v", err)
	}
}

func TestResolveFilePath(t *testing.T) {
	tests := []struct {
		mount string
		path  string
		want  string
	}{
		{"/", "src/main.c", "/src/main.c"},
		{"", "/src/main.c", "/src/main.c"},
		{"/mnt", "data/file.txt", "/mnt/data/file.txt"},
		{"", "", ""},
	}
	for _, tc := range tests {
		got := resolveFilePath(tc.mount, tc.path)
		if got != tc.want {
			t.Errorf("resolveFilePath(%q, %q) = %q, want %q", tc.mount, tc.path, got, tc.want)
		}
	}
}

func TestBuildArgv(t *testing.T) {
	tests := []struct {
		binary string
		args   string
		want   []string
	}{
		{"/bin/sh", "-c make", []string{"/bin/sh", "-c", "make"}},
		{"/usr/bin/gcc", "-o main main.c", []string{"/usr/bin/gcc", "-o", "main", "main.c"}},
		{"/bin/echo", `"hello world"`, []string{"/bin/echo", "hello world"}},
		{"", "", nil},
	}
	for _, tc := range tests {
		got := buildArgv(tc.binary, tc.args)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("buildArgv(%q, %q) mismatch (-want +got):\n%s", tc.binary, tc.args, diff)
		}
	}
}

func TestPermToEventType(t *testing.T) {
	tests := []struct {
		perm int
		want sgevpb.ResourceEvent_EventType
	}{
		{0x04, sgevpb.ResourceEvent_EVENT_TYPE_INPUT},
		{0x02, sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT},
		{0x06, sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT},
		{0x00, sgevpb.ResourceEvent_EVENT_TYPE_INPUT},
	}
	for _, tc := range tests {
		got := permToEventType(tc.perm)
		if got != tc.want {
			t.Errorf("permToEventType(%d) = %v, want %v", tc.perm, got, tc.want)
		}
	}
}
