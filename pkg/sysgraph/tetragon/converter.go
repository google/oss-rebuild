// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package tetragon converts raw tetragon events into SysGraph IR events.
package tetragon

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	tetragonpb "github.com/cilium/tetragon/api/v1/tetragon"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgir"
	"github.com/google/shlex"
	"google.golang.org/protobuf/proto"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	mayRead  = 0x04
	mayWrite = 0x02

	protRead  = 0x01
	protWrite = 0x02

	oWronly = 0x01
	oRdwr   = 0x02
	oCreat  = 0x40
	oTrunc  = 0x200
	oAppend = 0x400
)

// Converter holds state for the event conversion process, preventing
// duplicate emission of process-level events.
type Converter struct {
	mu            sync.Mutex
	seenProcesses map[string]bool
	// StoreRawEvents enables WriteRawEvents calls during conversion.
	// When false (the default), the overhead of serializing every
	// tetragon event as an anypb.Any is avoided.
	StoreRawEvents bool
}

// NewConverter initializes a new stateful event converter.
func NewConverter() *Converter {
	return &Converter{
		seenProcesses: make(map[string]bool),
	}
}

// Convert processes a batch of tetragon GetEventsResponse events and writes
// the corresponding SysGraph IR events to the given writer.
func (c *Converter) Convert(ctx context.Context, events []*tetragonpb.GetEventsResponse, w sgir.Writer) error {
	for _, event := range events {
		if err := c.ConvertEvent(ctx, event, w); err != nil {
			return fmt.Errorf("converting event: %w", err)
		}
	}
	return nil
}

// ConvertEvent processes a single tetragon event and writes the corresponding
// SysGraph IR events to the given writer. This is suitable for streaming use.
func (c *Converter) ConvertEvent(ctx context.Context, event *tetragonpb.GetEventsResponse, w sgir.Writer) error {
	var bgEvents []*sgpb.SysGraphEvent
	var actionID string

	switch ev := event.Event.(type) {
	case *tetragonpb.GetEventsResponse_ProcessExec:
		actionID = ev.ProcessExec.GetProcess().GetExecId()
		if parent := ev.ProcessExec.GetParent(); parent != nil {
			bgEvents = append(bgEvents, c.eventsFromProcess(parent)...)
		}
		process := ev.ProcessExec.GetProcess()
		bgEvents = append(bgEvents, sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(process.GetExecId()),
			Timestamp: ev.ProcessExec.GetProcess().GetStartTime(),
			MetadataEvent: sgpb.MetadataEvent_builder{
				Key:   proto.String("is_execve"),
				Value: proto.String("true"),
			}.Build(),
		}.Build())
		bgEvents = append(bgEvents, c.eventsFromProcess(ev.ProcessExec.GetProcess())...)

	case *tetragonpb.GetEventsResponse_ProcessExit:
		actionID = ev.ProcessExit.GetProcess().GetExecId()
		if parent := ev.ProcessExit.GetParent(); parent != nil {
			bgEvents = append(bgEvents, c.eventsFromProcess(parent)...)
		}
		bgEvents = append(bgEvents, c.eventsFromProcess(ev.ProcessExit.GetProcess())...)

		ts := ev.ProcessExit.GetTime()
		if ts == nil {
			ts = event.GetTime()
		}
		if actionID != "" {
			bgEvents = append(bgEvents, sgpb.SysGraphEvent_builder{
				ActionId:  proto.String(actionID),
				Timestamp: ts,
				EndEvent: sgpb.EndEvent_builder{
					Timestamp: ts,
					Status:    proto.Uint32(ev.ProcessExit.GetStatus()),
					Signal:    proto.String(ev.ProcessExit.GetSignal()),
				}.Build(),
			}.Build())
		}

	case *tetragonpb.GetEventsResponse_ProcessKprobe:
		actionID = ev.ProcessKprobe.GetProcess().GetExecId()
		if parent := ev.ProcessKprobe.GetParent(); parent != nil {
			bgEvents = append(bgEvents, c.eventsFromProcess(parent)...)
		}
		bgEvents = append(bgEvents, c.eventsFromProcess(ev.ProcessKprobe.GetProcess())...)

		kprobeEvents := convertProcessKprobe(actionID, event.GetTime(), ev.ProcessKprobe)
		bgEvents = append(bgEvents, kprobeEvents...)

	case *tetragonpb.GetEventsResponse_ProcessTracepoint:
		actionID = ev.ProcessTracepoint.GetProcess().GetExecId()
		if parent := ev.ProcessTracepoint.GetParent(); parent != nil {
			bgEvents = append(bgEvents, c.eventsFromProcess(parent)...)
		}
		bgEvents = append(bgEvents, c.eventsFromProcess(ev.ProcessTracepoint.GetProcess())...)

		tpEvents := convertProcessTracepoint(actionID, event.GetTime(), ev.ProcessTracepoint)
		bgEvents = append(bgEvents, tpEvents...)
	}

	if len(bgEvents) > 0 {
		if _, err := w.WriteEvents(ctx, bgEvents...); err != nil {
			return err
		}
	}

	if actionID != "" && c.StoreRawEvents {
		if _, err := w.WriteRawEvents(ctx, actionID, event); err != nil {
			return err
		}
	}

	return nil
}

// eventsFromProcess generates Start, Exec, Metadata, and Child events for a given process.
// It uses internal state to ensure these are only emitted once per process.
func (c *Converter) eventsFromProcess(process *tetragonpb.Process) []*sgpb.SysGraphEvent {
	if process == nil || process.GetExecId() == "" {
		return nil
	}

	execID := process.GetExecId()

	c.mu.Lock()
	if c.seenProcesses[execID] {
		c.mu.Unlock()
		return nil
	}
	c.seenProcesses[execID] = true
	c.mu.Unlock()

	ts := process.GetStartTime()
	argv := buildArgv(process.GetBinary(), process.GetArguments())

	events := []*sgpb.SysGraphEvent{
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(execID),
			Timestamp: ts,
			ExecEvent: sgpb.ExecEvent_builder{
				Executable: sgpb.Resource_builder{
					Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
					FileInfo: sgpb.FileInfo_builder{
						Path: proto.String(process.GetBinary()),
						Type: sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
					}.Build(),
				}.Build(),
				ExecInfo: sgpb.ExecInfo_builder{
					Argv:             argv,
					WorkingDirectory: proto.String(process.GetCwd()),
					Pid:              proto.Int64(int64(process.GetPid().GetValue())),
					Tid:              proto.Int64(int64(process.GetTid().GetValue())),
				}.Build(),
			}.Build(),
		}.Build(),
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(execID),
			Timestamp: ts,
			StartEvent: sgpb.StartEvent_builder{
				Timestamp: ts,
			}.Build(),
		}.Build(),
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(execID),
			Timestamp: ts,
			MetadataEvent: sgpb.MetadataEvent_builder{
				Key:   proto.String("exec_id"),
				Value: proto.String(execID),
			}.Build(),
		}.Build(),
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(execID),
			Timestamp: ts,
			MetadataEvent: sgpb.MetadataEvent_builder{
				Key:   proto.String("psns"),
				Value: proto.String(fmt.Sprintf("%d", process.GetNs().GetPid().GetInum())),
			}.Build(),
		}.Build(),
	}

	if docker := process.GetDocker(); docker != "" {
		events = append(events, sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(execID),
			Timestamp: ts,
			MetadataEvent: sgpb.MetadataEvent_builder{
				Key:   proto.String("docker"),
				Value: proto.String(docker), // The 15 first digits of the container ID.
			}.Build(),
		}.Build())
	}

	if parentExecID := process.GetParentExecId(); parentExecID != "" {
		events = append(events, sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(parentExecID),
			Timestamp: ts,
			ChildEvent: sgpb.ChildEvent_builder{
				ChildActionId: proto.String(execID),
			}.Build(),
		}.Build())
	}

	return events
}

func convertProcessKprobe(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	if actionID == "" {
		return nil
	}

	fn := trimSyscallPrefix(kprobe.GetFunctionName())
	switch fn {
	case "security_file_permission":
		return convertSecurityFilePermission(actionID, ts, kprobe)
	case "security_mmap_file":
		return convertSecurityMmapFile(actionID, ts, kprobe)
	case "security_path_truncate":
		return convertSecurityPathTruncate(actionID, ts, kprobe)
	case "security_path_rename":
		return convertSecurityPathRename(actionID, ts, kprobe)
	case "security_path_unlink":
		return convertSecurityPathUnlink(actionID, ts, kprobe)
	case "sys_pipe", "sys_pipe2":
		return convertPipeSyscall(actionID, ts, kprobe)
	case "sys_dup", "sys_dup2", "sys_dup3":
		return convertDupSyscall(actionID, ts, kprobe)
	default:
		log.Printf("unknown kprobe function name: %q", kprobe.GetFunctionName())
		return nil
	}
}

func convertSecurityFilePermission(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 2 || args[0].GetFileArg() == nil {
		return nil
	}
	filePath := resolveFilePath(args[0].GetFileArg().GetMount(), args[0].GetFileArg().GetPath())
	if filePath == "" {
		return nil
	}
	permArg := args[1].GetIntArg()
	return buildResourceEvent(actionID, ts, filePath, permToEventType(int(permArg)))
}

func convertSecurityMmapFile(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 2 || args[0].GetFileArg() == nil {
		return nil
	}
	filePath := resolveFilePath(args[0].GetFileArg().GetMount(), args[0].GetFileArg().GetPath())
	if filePath == "" {
		return nil
	}
	protFlags := args[1].GetUintArg()
	return buildResourceEvent(actionID, ts, filePath, protToEventType(protFlags))
}

func convertSecurityPathTruncate(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 1 || args[0].GetPathArg() == nil {
		return nil
	}
	filePath := resolveFilePath(args[0].GetPathArg().GetMount(), args[0].GetPathArg().GetPath())
	if filePath == "" {
		return nil
	}
	return buildResourceEvent(actionID, ts, filePath, sgpb.ResourceEvent_EVENT_TYPE_OUTPUT)
}

func convertSecurityPathRename(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 4 || args[0].GetPathArg() == nil || args[1].GetPathArg() == nil || args[2].GetPathArg() == nil || args[3].GetPathArg() == nil {
		return nil
	}
	oldDir := resolveFilePath(args[0].GetPathArg().GetMount(), args[0].GetPathArg().GetPath())
	oldName := args[1].GetPathArg().GetPath()
	newDir := resolveFilePath(args[2].GetPathArg().GetMount(), args[2].GetPathArg().GetPath())
	newName := args[3].GetPathArg().GetPath()
	oldPath := filepath.Join(oldDir, oldName)
	newPath := filepath.Join(newDir, newName)
	if oldPath == "" && newPath == "" {
		return nil
	}
	var events []*sgpb.SysGraphEvent
	if oldPath != "" {
		events = append(events, buildResourceEvent(actionID, ts, oldPath, sgpb.ResourceEvent_EVENT_TYPE_OUTPUT)...)
	}
	if newPath != "" {
		events = append(events, buildResourceEvent(actionID, ts, newPath, sgpb.ResourceEvent_EVENT_TYPE_OUTPUT)...)
	}
	return events
}

func convertSecurityPathUnlink(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 2 || args[0].GetPathArg() == nil || args[1].GetPathArg() == nil {
		return nil
	}
	dir := resolveFilePath(args[0].GetPathArg().GetMount(), args[0].GetPathArg().GetPath())
	name := args[1].GetPathArg().GetPath()
	filePath := filepath.Join(dir, name)
	if filePath == "" {
		return nil
	}
	return buildResourceEvent(actionID, ts, filePath, sgpb.ResourceEvent_EVENT_TYPE_OUTPUT)
}

func convertPipeSyscall(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	return []*sgpb.SysGraphEvent{
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(actionID),
			Timestamp: ts,
			PipeEvent: sgpb.PipeEvent_builder{}.Build(),
		}.Build(),
	}
}

func convertDupSyscall(actionID string, ts *tpb.Timestamp, kprobe *tetragonpb.ProcessKprobe) []*sgpb.SysGraphEvent {
	args := kprobe.GetArgs()
	if len(args) < 2 {
		return nil
	}
	oldFd := args[0].GetIntArg()
	newFd := args[1].GetIntArg()
	parentExecID := kprobe.GetProcess().GetParentExecId()

	return []*sgpb.SysGraphEvent{
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(actionID),
			Timestamp: ts,
			DupEvent: sgpb.DupEvent_builder{
				OldFd:        proto.Int32(oldFd),
				NewFd:        proto.Int32(newFd),
				ParentExecId: proto.String(parentExecID),
				Timestamp:    kprobe.GetProcess().GetStartTime(),
			}.Build(),
		}.Build(),
	}
}

func convertProcessTracepoint(actionID string, ts *tpb.Timestamp, tp *tetragonpb.ProcessTracepoint) []*sgpb.SysGraphEvent {
	if actionID == "" {
		return nil
	}
	// TODO: Handle necessary tracepoints
	return nil
}

func buildResourceEvent(actionID string, ts *tpb.Timestamp, filePath string, eventType sgpb.ResourceEvent_EventType) []*sgpb.SysGraphEvent {
	return []*sgpb.SysGraphEvent{
		sgpb.SysGraphEvent_builder{
			ActionId:  proto.String(actionID),
			Timestamp: ts,
			ResourceEvent: sgpb.ResourceEvent_builder{
				EventType: eventType.Enum(),
				Resource: sgpb.Resource_builder{
					Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
					FileInfo: sgpb.FileInfo_builder{
						Path: proto.String(filePath),
						Type: sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
					}.Build(),
				}.Build(),
			}.Build(),
		}.Build(),
	}
}

// syscallPrefixes are architecture-specific prefixes that the kernel prepends
// to syscall entry points. Tetragon reports the full symbol name, so we strip
// these to normalize function names across architectures.
var syscallPrefixes = []string{
	"__x64_",   // amd64
	"__arm64_", // arm64
	"__ia32_",  // i386
}

func trimSyscallPrefix(fn string) string {
	for _, p := range syscallPrefixes {
		if strings.HasPrefix(fn, p) {
			return fn[len(p):]
		}
	}
	return fn
}

// resolveFilePath combines mount and path into a full file path.
func resolveFilePath(mount, path string) string {
	if path == "" {
		return ""
	}
	if mount == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(mount, path))
}

// buildArgv builds an argv slice from binary and arguments string using shlex.
func buildArgv(binary, arguments string) []string {
	if binary == "" {
		return nil
	}
	args, err := shlex.Split(arguments)
	if err != nil || len(args) == 0 {
		args = []string{arguments}
	}

	// Ensure binary is the 0th element.
	if len(args) == 0 || args[0] != binary {
		args = append([]string{binary}, args...)
	}
	return args
}

// permToEventType converts kernel MAY_READ/MAY_WRITE permission masks to event types.
func permToEventType(perm int) sgpb.ResourceEvent_EventType {
	if perm&mayWrite != 0 {
		return sgpb.ResourceEvent_EVENT_TYPE_OUTPUT
	}
	return sgpb.ResourceEvent_EVENT_TYPE_INPUT
}

// protToEventType converts mmap PROT_READ/PROT_WRITE flags to event types.
func protToEventType(prot uint32) sgpb.ResourceEvent_EventType {
	if prot&protWrite != 0 {
		return sgpb.ResourceEvent_EVENT_TYPE_OUTPUT
	}
	return sgpb.ResourceEvent_EVENT_TYPE_INPUT
}
