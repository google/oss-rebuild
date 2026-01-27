// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgstorage

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"log"

	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	anypb "google.golang.org/protobuf/types/known/anypb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

// SysGraphBuilder is a builder for sysgraphs.
type SysGraphBuilder struct {
	GraphPb          *sgpb.SysGraph
	RootActionFilter func(*sgpb.Action) bool

	mu        sync.Mutex
	resources map[pbdigest.Digest]*sgpb.Resource
	actions   map[int64]*ActionBuilder
	aids      map[string]int64

	// JSON serialized events from tetragon for each action.
	rawEvents map[int64][]*anypb.Any
}

// ActionID creates a new action id, the same id will always be returned for the same string.
func (b *SysGraphBuilder) actionID(sid string) int64 {
	if b.aids == nil {
		b.aids = make(map[string]int64)
	}
	if id, ok := b.aids[sid]; ok {
		return id
	}
	id := int64(len(b.aids) + 1)
	b.aids[sid] = id
	return id
}

// Action returns the action with the given id.
// sid is an arbitrary string that identifies the action during building
// and will not be persisted after Build() is called.
func (b *SysGraphBuilder) Action(sid string) *ActionBuilder {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.actions == nil {
		b.actions = make(map[int64]*ActionBuilder)
	}
	id := b.actionID(sid)
	if a, ok := b.actions[id]; ok {
		return a
	}
	a := &ActionBuilder{
		sgbuilder: b,
		id:        id,
	}
	b.actions[id] = a
	return a
}

// AddRawEvent adds a raw event to the sysgraph.
func (b *SysGraphBuilder) AddRawEvent(sid string, event proto.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rawEvents == nil {
		b.rawEvents = make(map[int64][]*anypb.Any)
	}
	id := b.actionID(sid)
	anyEvent, err := anypb.New(event)
	if err != nil {
		log.Printf("Failed to marshal event %v: %v", event, err)
		return
	}
	b.rawEvents[id] = append(b.rawEvents[id], anyEvent)
}

func (b *SysGraphBuilder) addResource(resource *sgpb.Resource) (pbdigest.Digest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dg, err := pbdigest.NewFromMessage(resource)
	if err != nil {
		return pbdigest.Digest{}, fmt.Errorf("failed to create digest for resource %v: %v", resource, err)
	}
	if b.resources == nil {
		b.resources = make(map[pbdigest.Digest]*sgpb.Resource)
	}
	b.resources[dg] = resource
	return dg, nil
}

// ActionExists returns true if the action with the given id exists.
func (b *SysGraphBuilder) ActionExists(sid string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aids == nil {
		return false
	}
	_, ok := b.aids[sid]
	return ok
}

// ActionBuilder is a builder for actions.
type ActionBuilder struct {
	sgbuilder *SysGraphBuilder
	id        int64
	// StartTime will populate Action.start_time
	StartTime time.Time
	// EndTime will populate Action.end_time
	EndTime time.Time
	// ExecInfo will populate Action.exec_info
	ExecInfo *sgpb.ExecInfo
	// ActionInteraction fields from sgpb.Action
	children map[int64]*sgpb.ActionInteraction
	// Parent fields from sgpb.Action
	parentID int64
	parent   *sgpb.ActionInteraction
	// ResourceInteraction fields from sgpb.Action
	executableDg string
	executable   *sgpb.ResourceInteraction
	inputs       map[string]*sgpb.ResourceInteractions
	outputs      map[string]*sgpb.ResourceInteractions
	metadata     map[string]string
	mu           sync.Mutex
}

// ID returns the id of the action.
func (b *ActionBuilder) ID() int64 {
	return b.id
}

// SetParent adds a child action to the action.
// interaction will be added to the parent action's children field and as this action's parent field
// with the action id filled in correctly.
func (b *ActionBuilder) SetParent(parent string, interaction *sgpb.ActionInteraction) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.parent != nil {
		return fmt.Errorf("action already has a parent")
	}
	parentAction := b.sgbuilder.Action(parent)
	if parentAction.id == b.id {
		return fmt.Errorf("action cannot be its own parent")
	}
	parentAction.mu.Lock()
	defer parentAction.mu.Unlock()
	b.parentID = parentAction.id
	b.parent = proto.Clone(interaction).(*sgpb.ActionInteraction)
	if parentAction.children == nil {
		parentAction.children = make(map[int64]*sgpb.ActionInteraction)
	}
	parentAction.children[b.id] = proto.Clone(interaction).(*sgpb.ActionInteraction)
	return nil
}

// AddInput adds an input to the action.
// interaction.resource_digest will be filled in with the actual digest of the resource.
func (b *ActionBuilder) AddInput(resource *sgpb.Resource, interaction *sgpb.ResourceInteraction) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dg, err := b.sgbuilder.addResource(resource)
	if err != nil {
		return err
	}
	if b.inputs == nil {
		b.inputs = make(map[string]*sgpb.ResourceInteractions)
	}
	if o, ok := b.inputs[dg.String()]; !ok {
		b.inputs[dg.String()] = sgpb.ResourceInteractions_builder{
			Interactions: []*sgpb.ResourceInteraction{interaction},
		}.Build()
	} else {
		o.SetInteractions(append(o.GetInteractions(), interaction))
	}
	return nil
}

// AddOutput adds an output to the action.
// interaction.resource_digest will be filled in with the actual digest of the resource.
func (b *ActionBuilder) AddOutput(resource *sgpb.Resource, interaction *sgpb.ResourceInteraction) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dg, err := b.sgbuilder.addResource(resource)
	if err != nil {
		return err
	}
	interaction = proto.Clone(interaction).(*sgpb.ResourceInteraction)
	if b.outputs == nil {
		b.outputs = make(map[string]*sgpb.ResourceInteractions)
	}
	if o, ok := b.outputs[dg.String()]; !ok {
		b.outputs[dg.String()] = sgpb.ResourceInteractions_builder{
			Interactions: []*sgpb.ResourceInteraction{interaction},
		}.Build()
	} else {
		o.SetInteractions(append(o.GetInteractions(), interaction))
	}
	return nil
}

// SetExecutable sets the executable for the action.
// interaction.resource_digest will be filled in with the actual digest of the resource.
func (b *ActionBuilder) SetExecutable(resource *sgpb.Resource, interaction *sgpb.ResourceInteraction) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dg, err := b.sgbuilder.addResource(resource)
	if err != nil {
		return err
	}
	interaction = proto.Clone(interaction).(*sgpb.ResourceInteraction)
	b.executableDg = dg.String()
	b.executable = interaction
	return nil
}

// SetMetadata sets the metadata for the action.
func (b *ActionBuilder) SetMetadata(key, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.metadata == nil {
		b.metadata = make(map[string]string)
	}
	b.metadata[key] = value
}

func cloneProtoMap[K comparable, T proto.Message](s map[K]T) map[K]T {
	cloned := make(map[K]T, len(s))
	for k, t := range s {
		cloned[k] = proto.Clone(t).(T)
	}
	return cloned
}

func maybeString(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}

func maybeInt64(i int64) *int64 {
	if i == 0 {
		return nil
	}
	return proto.Int64(i)
}

func (b *ActionBuilder) toProto() *sgpb.Action {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sgpb.Action_builder{
		Id:                       proto.Int64(b.id),
		SysGraphId:               maybeString(b.sgbuilder.GraphPb.GetId()),
		StartTime:                tpb.New(b.StartTime),
		EndTime:                  tpb.New(b.EndTime),
		ExecInfo:                 proto.Clone(b.ExecInfo).(*sgpb.ExecInfo),
		Children:                 cloneProtoMap(b.children),
		ParentActionId:           maybeInt64(b.parentID),
		Parent:                   proto.Clone(b.parent).(*sgpb.ActionInteraction),
		ExecutableResourceDigest: maybeString(b.executableDg),
		Executable:               proto.Clone(b.executable).(*sgpb.ResourceInteraction),
		Inputs:                   cloneProtoMap(b.inputs),
		Outputs:                  cloneProtoMap(b.outputs),
		Metadata:                 maps.Clone(b.metadata),
	}.Build()
}

// dfs performs a depth first search of the action tree starting at aid.
// flatAIDs is filled with all the actions in the tree starting from aid.
func (b *SysGraphBuilder) dfs(aid int64, tree map[int64][]int64, flatAIDs map[int64]bool) {
	flatAIDs[aid] = true
	if _, ok := tree[aid]; !ok {
		return
	}
	for _, child := range tree[aid] {
		b.dfs(child, tree, flatAIDs)
	}
}

// Build builds the sysgraph.
func (b *SysGraphBuilder) Build(ctx context.Context) *inmemory.SysGraph {
	log.Printf("Building sysgraph with %d actions and %d resources", len(b.actions), len(b.resources))
	b.mu.Lock()
	defer b.mu.Unlock()
	actions := make(map[int64]*sgpb.Action, len(b.actions))
	var entryPoints []int64
	for _, a := range b.actions {
		actions[a.id] = a.toProto()
		if !actions[a.id].HasParentActionId() {
			entryPoints = append(entryPoints, a.id)
		}
	}
	if b.GraphPb == nil {
		b.GraphPb = &sgpb.SysGraph{}
	}
	if b.GraphPb.GetId() == "" {
		// All sysgraphs should have a unique id.
		log.Printf("sysgraph id is empty, setting to a new uuid")
		b.GraphPb.SetId(uuid.New().String())
	}
	b.GraphPb.SetEntryPointActionIds(entryPoints)
	resources := make(map[pbdigest.Digest]*sgpb.Resource, len(b.resources))
	for dg, r := range b.resources {
		resources[dg] = proto.Clone(r).(*sgpb.Resource)
	}
	log.Printf("Constructed %d actions and %d resources", len(actions), len(resources))
	return &inmemory.SysGraph{
		GraphPb:     b.GraphPb,
		Actions:     actions,
		ResourceMap: b.resources,
		Events:      b.rawEvents,
	}
}
