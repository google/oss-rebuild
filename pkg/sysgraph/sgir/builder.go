// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgir

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgevpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

type parent struct {
	actionID  int64
	timestamp time.Time
}

type copiedFD struct {
	execID    string
	timestamp time.Time
	oldFD     int32
	newFD     int32
}

type pipeCommunication struct {
	// action ids for all the risky pipe communication actions
	actionIDs map[int64]struct{}
	// dup syscall's tetragon exec id (key) -> the pipe resource that this dup action is a reader of.
	readers map[string]*sgpb.Resource
	// dup syscall's tetragon exec id (key) -> the pipe resource that this dup action is a writer of.
	// We need to have two maps here because a dup's exec id can be both a reader and a writer at the
	// same time if it is in the middle of a chain of pipe actions.
	writers map[string]*sgpb.Resource
}

// syncRdb is a thread-safe resource database.
type syncRdb struct {
	m syncx.Map[string, *sgpb.Resource]
}

func (rdb *syncRdb) addResource(resource *sgpb.Resource) (pbdigest.Digest, error) {
	dg, err := pbdigest.NewFromMessage(resource)
	if err != nil {
		return pbdigest.Digest{}, err
	}
	rdb.m.Store(dg.String(), resource)
	return dg, nil
}

func (rdb *syncRdb) Proto() (*sgpb.ResourceDB, error) {
	db := make(map[string]*sgpb.Resource)
	rdb.m.Range(func(k string, v *sgpb.Resource) bool {
		db[k] = v
		return true
	})
	return sgpb.ResourceDB_builder{Resources: db}.Build(), nil
}

type actionBuilderOptions struct {
	irActionID string
	events     []*sgevpb.SysGraphEvent
	riskyPipes *pipeCommunication
	parents    map[string]parent
	sidToID    map[string]int64
	rdb        *syncRdb
	graphID    string
}

func (b actionBuilderOptions) buildAction(ctx context.Context) (*sgpb.Action, error) {
	aid, ok := b.sidToID[b.irActionID]
	if !ok {
		return nil, fmt.Errorf("action id %q not found in sidToID map", b.irActionID)
	}
	builder := sgpb.Action_builder{
		Id:         proto.Int64(aid),
		SysGraphId: proto.String(b.graphID),
	}
	if p, ok := b.parents[b.irActionID]; ok {
		builder.ParentActionId = proto.Int64(p.actionID)
		builder.Parent = sgpb.ActionInteraction_builder{
			Timestamp: tpb.New(p.timestamp),
		}.Build()
	}
	inputs := make(map[string][]*sgpb.ResourceInteraction)
	outputs := make(map[string][]*sgpb.ResourceInteraction)
	for _, event := range b.events {
		switch event.WhichEvent() {
		case sgevpb.SysGraphEvent_ResourceEvent_case:
			resourceEvent := event.GetResourceEvent()
			dg, err := b.rdb.addResource(resourceEvent.GetResource())
			if err != nil {
				return nil, err
			}
			switch resourceEvent.GetEventType() {
			case sgevpb.ResourceEvent_EVENT_TYPE_INPUT:
				inputs[dg.String()] = append(inputs[dg.String()], sgpb.ResourceInteraction_builder{
					Timestamp: event.GetTimestamp(),
					IoInfo:    resourceEvent.GetIoInfo(),
				}.Build())
			case sgevpb.ResourceEvent_EVENT_TYPE_OUTPUT:
				outputs[dg.String()] = append(outputs[dg.String()], sgpb.ResourceInteraction_builder{
					Timestamp: event.GetTimestamp(),
					IoInfo:    resourceEvent.GetIoInfo(),
				}.Build())
			}
		case sgevpb.SysGraphEvent_ExecEvent_case:
			execEvent := event.GetExecEvent()
			dg, err := b.rdb.addResource(execEvent.GetExecutable())
			if err != nil {
				return nil, err
			}
			builder.ExecutableResourceDigest = proto.String(dg.String())
			builder.Executable = sgpb.ResourceInteraction_builder{
				Timestamp: event.GetTimestamp(),
			}.Build()
			builder.ExecInfo = execEvent.GetExecInfo()
		case sgevpb.SysGraphEvent_MetadataEvent_case:
			metadataEvent := event.GetMetadataEvent()
			if builder.Metadata == nil {
				builder.Metadata = make(map[string]string)
			}
			builder.Metadata[metadataEvent.GetKey()] = metadataEvent.GetValue()
		case sgevpb.SysGraphEvent_ChildEvent_case:
			childEvent := event.GetChildEvent()
			childActionID, ok := b.sidToID[childEvent.GetChildActionId()]
			if !ok {
				return nil, fmt.Errorf("child action id %q not found in sidToID map", childEvent.GetChildActionId())
			}
			if builder.Children == nil {
				builder.Children = make(map[int64]*sgpb.ActionInteraction)
			}
			builder.Children[childActionID] = sgpb.ActionInteraction_builder{
				Timestamp: event.GetTimestamp(),
			}.Build()
		case sgevpb.SysGraphEvent_StartEvent_case:
			builder.StartTime = event.GetStartEvent().GetTimestamp()
		case sgevpb.SysGraphEvent_EndEvent_case:
			builder.EndTime = event.GetEndEvent().GetTimestamp()
			builder.ExitSignal = proto.String(event.GetEndEvent().GetSignal())
			builder.ExitStatus = proto.Uint32(event.GetEndEvent().GetStatus())
		case sgevpb.SysGraphEvent_PipeEvent_case:
			if b.riskyPipes != nil {
				//  actionIDs are guaranteed not nil.
				if _, ok := b.riskyPipes.actionIDs[aid]; ok {
					if builder.Metadata == nil {
						builder.Metadata = make(map[string]string)
					}
					builder.Metadata["risky_pipe"] = "true"
				}
			}
		case sgevpb.SysGraphEvent_DupEvent_case:
			if b.riskyPipes != nil {
				//  readers/writers are guaranteed not nil.
				if pipeResource, ok := b.riskyPipes.readers[b.irActionID]; ok {
					pipeDg, err := b.rdb.addResource(pipeResource)
					if err != nil {
						return nil, err
					}
					inputs[pipeDg.String()] = append(inputs[pipeDg.String()], sgpb.ResourceInteraction_builder{
						Timestamp: event.GetTimestamp(),
					}.Build())
				}
				if pipeResource, ok := b.riskyPipes.writers[b.irActionID]; ok {
					pipeDg, err := b.rdb.addResource(pipeResource)
					if err != nil {
						return nil, err
					}
					outputs[pipeDg.String()] = append(outputs[pipeDg.String()], sgpb.ResourceInteraction_builder{
						Timestamp: event.GetTimestamp(),
					}.Build())
				}
			}
		}
	}
	if len(inputs) > 0 {
		builder.Inputs = map[string]*sgpb.ResourceInteractions{}
		for dg, input := range inputs {
			builder.Inputs[dg] = sgpb.ResourceInteractions_builder{
				Interactions: input,
			}.Build()
		}
	}
	if len(outputs) > 0 {
		builder.Outputs = map[string]*sgpb.ResourceInteractions{}
		for dg, output := range outputs {
			builder.Outputs[dg] = sgpb.ResourceInteractions_builder{
				Interactions: output,
			}.Build()
		}
	}
	return builder.Build(), nil
}

func (c *Builder) parents(ctx context.Context, ep Reader, irToBGActionIDMap map[string]int64) (map[string]parent, *pipeCommunication, error) {
	// All the pipe actions in this build, including both the risky and non-risky ones.
	var pipeActions syncx.Map[int64, struct{}]
	// All the dup actions that redirects stdin or stdout.
	// The parent action of these dup actions may or may not contain a pipe syscall being made.
	dupActions := make(map[int64][]copiedFD)
	var dupActionsMu sync.Mutex
	var parents syncx.Map[string, parent]
	eg, errCtx := c.eg(ctx)
	for irActionID := range irToBGActionIDMap {
		eg.Go(func() error {
			c.progressFunc()
			defer c.progressFunc()
			events, err := ep.Events(errCtx, irActionID)
			if err != nil {
				return err
			}
			aid, ok := irToBGActionIDMap[irActionID]
			if !ok {
				return fmt.Errorf("ir action id %q not found in irToBGActionIDMap map", irActionID)
			}
			for _, event := range events {
				switch event.WhichEvent() {
				case sgevpb.SysGraphEvent_ChildEvent_case:
					childEvent := event.GetChildEvent()
					parents.Store(childEvent.GetChildActionId(), parent{
						actionID:  aid,
						timestamp: event.GetTimestamp().AsTime(),
					})
				case sgevpb.SysGraphEvent_PipeEvent_case:
					pipeActions.Store(aid, struct{}{})
				case sgevpb.SysGraphEvent_DupEvent_case:
					// The string, parentExecID, is the tetragon exec ID, also the IR format's action ID.
					// The int64, parentActID, is the SysGraph action ID.
					parentExecID := event.GetDupEvent().GetParentExecId()
					parentActID, ok := irToBGActionIDMap[parentExecID]
					if !ok {
						return fmt.Errorf("parent exec id %q not found in irToBGActionIDMap map", parentExecID)
					}
					dup := copiedFD{
						execID:    irActionID,
						timestamp: event.GetDupEvent().GetTimestamp().AsTime(),
						oldFD:     event.GetDupEvent().GetOldFd(),
						newFD:     event.GetDupEvent().GetNewFd(),
					}
					dupActionsMu.Lock()
					dupActions[parentActID] = append(dupActions[parentActID], dup)
					dupActionsMu.Unlock()
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}
	// Filter all the dup actions which are children of pipe actions. If the parent action is not a
	// pipe action, we just drop the dup action as there will be no security concerns for normal
	// dup syscall.
	dupActionsSync := make(map[int64][]copiedFD)
	for key, value := range dupActions {
		if _, seen := pipeActions.Load(key); seen {
			list := value
			sort.Slice(list, func(i, j int) bool {
				return list[i].timestamp.Before(list[j].timestamp)
			})
			dupActionsSync[key] = list
		}
	}
	parentsSync := make(map[string]parent)
	parents.Range(func(k string, v parent) bool {
		parentsSync[k] = v
		return true
	})
	riskyPipes, err := findRiskyPipes(dupActionsSync)
	if err != nil {
		return parentsSync, nil, err
	}
	return parentsSync, riskyPipes, nil
}

// findRiskyPipes finds all the risky pipes in the sysgraph.
// A pipe is considered risky if it has more than 2 dup child actions and the child dup actions
// are forming a chain of dup(x, 1) dup(y, 0) pairs so that they can communicate with each other.
func findRiskyPipes(dupActions map[int64][]copiedFD) (*pipeCommunication, error) {
	p := make(map[int64]struct{})
	w := make(map[string]*sgpb.Resource)
	r := make(map[string]*sgpb.Resource)
	for id, dups := range dupActions {
		if len(dups) < 2 {
			continue
		}
		var writerExecID string
		var writeEndDup *sgpb.StdIODupInfo
		for _, d := range dups {
			newFd := d.newFD
			switch newFd {
			case 1:
				writerExecID = d.execID
				writeEndDup = sgpb.StdIODupInfo_builder{
					OldFd: proto.Int32(d.oldFD),
					NewFd: proto.Int32(newFd),
				}.Build()
			case 0:
				if writerExecID != "" && d.execID != writerExecID {
					// Build the pipe resource that both the writer and reader actions are interacting with.
					resource := sgpb.Resource_builder{
						Type: sgpb.ResourceType_RESOURCE_TYPE_PIPE.Enum(),
						PipeInfo: sgpb.PipeInfo_builder{
							ReadEnd: sgpb.StdIODupInfo_builder{
								OldFd: proto.Int32(d.oldFD),
								NewFd: proto.Int32(newFd),
							}.Build(),
							ReadExecId:  proto.String(d.execID),
							WriteEnd:    writeEndDup,
							WriteExecId: proto.String(writerExecID),
						}.Build(),
					}.Build()
					w[writerExecID] = resource
					r[d.execID] = resource
					p[id] = struct{}{}
					writerExecID = ""
					writeEndDup = nil
				}
			default:
				return nil, fmt.Errorf("dup action %d has invalid new fd %d (only 1 and 0 are valid)", id, newFd)
			}
		}
	}
	return &pipeCommunication{writers: w, readers: r, actionIDs: p}, nil
}

// Builder is a struct for constructing a sysgraph from an IR provider.
type Builder struct {
	// ConcurrencyLimit is the maximum number of go routines to use concurrently.
	ConcurrencyLimit int
	// StoreRawEvents controls whether the raw events are copied from the IR provider to the graph writer.
	StoreRawEvents bool
	// ProgressFunc is called to notify progress.
	progressFunc func()
}

func (c *Builder) eg(ctx context.Context) (*errgroup.Group, context.Context) {
	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(c.ConcurrencyLimit)
	return eg, ctx
}

// Reader is an interface for providing SysGraph IR events.
type Reader interface {
	// Events returns the IR events where SysGraphEvent.action_id == actionID.
	// Events are sorted by timestamp.
	Events(ctx context.Context, actionID string) ([]*sgevpb.SysGraphEvent, error)
	// Actions returns all values of SysGraphEvent.action_id.
	Actions(ctx context.Context) ([]string, error)
	// RawEvents returns the raw events for the action where SysGraphEvent.action_id == actionID.
	RawEvents(ctx context.Context, actionID string) (<-chan *anypb.Any, <-chan error, error)
}

// Some types that implement the Reader interface.
var _ Reader = (*InMemoryFormat)(nil)
var _ Reader = (*DiskFormat)(nil)

func (c *Builder) writeActions(ctx context.Context, baseOpts actionBuilderOptions, reader Reader, writer *sgstorage.GraphWriter) error {
	eg, errCtx := c.eg(ctx)
	for irActionID := range baseOpts.sidToID {
		opts := baseOpts
		eg.Go(func() error {
			c.progressFunc()
			defer c.progressFunc()
			events, err := reader.Events(ctx, irActionID)
			if err != nil {
				return err
			}
			opts.events = events
			opts.irActionID = irActionID
			action, err := opts.buildAction(errCtx)
			if err != nil {
				return err
			}
			return writer.WriteAction(errCtx, action)
		})
	}
	return eg.Wait()
}

func (c *Builder) writeRawEvents(ctx context.Context, sidToID map[string]int64, reader Reader, writer *sgstorage.GraphWriter) error {
	eg, errCtx := c.eg(ctx)
	for irActionID := range sidToID {
		eg.Go(func() error {
			rawEvents, errCh, err := reader.RawEvents(errCtx, irActionID)
			if err != nil {
				return err
			}
			if rawEvents != nil {
				if err := writer.WriteRawEvents(errCtx, sidToID[irActionID], rawEvents); err != nil {
					return err
				}
			}
			select {
			case <-errCtx.Done():
				return ctx.Err()
			case err, ok := <-errCh:
				if ok && err != nil {
					return err
				}
			default:
			}
			return nil
		})
	}
	return eg.Wait()
}

// ToSysGraph builds a sysgraph from an IR reader.
func (c *Builder) ToSysGraph(ctx context.Context, graphID string, reader Reader, graphPath string, opts ...sgstorage.Option) error {
	writer, err := sgstorage.NewGraphWriter(ctx, graphPath, opts...)
	if err != nil {
		return err
	}
	c.progressFunc = writer.ProgressFunc
	closeWriter := sync.OnceValue(writer.Close)
	defer closeWriter()
	if c.ConcurrencyLimit <= 0 {
		return fmt.Errorf("concurrency limit must be positive")
	}
	irActionIDs, err := reader.Actions(ctx)
	if err != nil {
		return err
	}

	// Sort the IR action IDs to ensure that the sysgraph action IDs are deterministic.
	sort.Strings(irActionIDs)
	sidToID := make(map[string]int64)
	for i, irActionID := range irActionIDs {
		sidToID[irActionID] = int64(i) + 1
	}

	parents, riskyPipes, err := c.parents(ctx, reader, sidToID)
	if err != nil {
		return err
	}

	rdb := &syncRdb{}
	baseOpts := actionBuilderOptions{
		riskyPipes: riskyPipes,
		parents:    parents,
		sidToID:    sidToID,
		rdb:        rdb,
		graphID:    graphID,
	}
	if err := c.writeActions(ctx, baseOpts, reader, writer); err != nil {
		return err
	}

	if c.StoreRawEvents {
		if err := c.writeRawEvents(ctx, sidToID, reader, writer); err != nil {
			return err
		}
	}

	rdbPb, err := rdb.Proto()
	if err != nil {
		return err
	}
	if err := writer.WriteRDB(ctx, rdbPb); err != nil {
		return err
	}

	entryPoints := make([]int64, 0, len(irActionIDs))
	for _, irActionID := range irActionIDs {
		if _, ok := parents[irActionID]; !ok {
			entryPoints = append(entryPoints, sidToID[irActionID])
		}
	}
	slices.Sort(entryPoints)

	if err := writer.WriteGraphProto(ctx, sgpb.SysGraph_builder{
		Id:                  proto.String(graphID),
		EntryPointActionIds: entryPoints,
	}.Build()); err != nil {
		return err
	}
	return closeWriter()
}
