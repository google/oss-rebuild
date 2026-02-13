// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgir

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	anypb "google.golang.org/protobuf/types/known/anypb"

	"maps"

	sgevpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Writer is an interface for writing IR events.
type Writer interface {
	WriteEvents(ctx context.Context, events ...*sgevpb.SysGraphEvent) (int64, error)
	WriteRawEvents(ctx context.Context, actionID string, rawEvents ...proto.Message) (int64, error)
}

var _ Writer = (*DiskFormat)(nil)
var _ Writer = (*InMemoryFormat)(nil)

// EventFileFormat is the format of the event file.
type EventFileFormat int

const (
	// PBDelim will read and write event files in google.golang.org/protobuf/encoding/protodelim format.
	PBDelim EventFileFormat = iota
	// JSONL will read and write event files in google.golang.org/protobuf/encoding/protojson format with a newline delimiter.
	JSONL
)

func (ef EventFileFormat) String() string {
	switch ef {
	case PBDelim:
		return "PBDelim"
	case JSONL:
		return "JSONL"
	default:
		return "Unknown"
	}
}

func (ef EventFileFormat) ext() string {
	switch ef {
	case PBDelim:
		return ".pbdelim"
	case JSONL:
		return ".jsonl"
	default:
		return ""
	}
}

func (ef EventFileFormat) appendMsg(f io.Writer, msg proto.Message) (int, error) {
	switch ef {
	case PBDelim:
		return protodelim.MarshalTo(f, msg)
	case JSONL:
		blob, err := protojson.Marshal(msg)
		if err != nil {
			return 0, err
		}
		n, err := f.Write(blob)
		if err != nil {
			return 0, err
		}
		_, err = f.Write([]byte("\n"))
		if err != nil {
			return n, err
		}
		return n + 1, nil
	default:
		return 0, fmt.Errorf("unknown event file format: %v", ef)
	}
}

func (ef EventFileFormat) unmarshalMsg(buf *bufio.Reader, msg proto.Message) error {
	switch ef {
	case PBDelim:
		return protodelim.UnmarshalFrom(buf, msg)
	case JSONL:
		blob, err := buf.ReadBytes('\n')
		if err != nil {
			return err
		}
		return protojson.Unmarshal(blob, msg)
	default:
		return fmt.Errorf("unknown event file format: %v", ef)
	}
}

// DiskFormat is an IR provider that provides IR events from disk.
// TODO: Refactor to use a virtual FS (e.g., go-billy) instead of direct disk access.
type DiskFormat struct {
	BasePath string
	Format   EventFileFormat
}

// RawEvents returns the raw events for the action.
func (ep *DiskFormat) RawEvents(ctx context.Context, actionID string) (<-chan *anypb.Any, <-chan error, error) {
	ch := make(chan *anypb.Any)
	errCh := make(chan error, 1)
	f, err := os.Open(filepath.Join(ep.BasePath, actionID+sgstorage.RawEventsFileNameSuffix+ep.Format.ext()))
	if err != nil {
		close(ch)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	buf := bufio.NewReader(f)
	go func() {
		defer f.Close()
		defer close(ch)
		for {
			event := &anypb.Any{}
			if err := ep.Format.unmarshalMsg(buf, event); err != nil {
				if err == io.EOF {
					return
				}
				errCh <- err
				return
			}
			ch <- event
		}
	}()
	return ch, errCh, nil
}

// Actions returns the actions in the event provider.
func (ep *DiskFormat) Actions(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(ep.BasePath)
	if err != nil {
		return nil, err
	}
	var sids []string
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		if strings.Contains(f.Name(), sgstorage.RawEventsFileNameSuffix) {
			continue
		}
		sids = append(sids, strings.TrimSuffix(filepath.Base(f.Name()), filepath.Ext(f.Name())))
	}
	return sids, nil
}

func readEvent(path string) (*sgevpb.SysGraphEvent, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	event := &sgevpb.SysGraphEvent{}
	if err := proto.Unmarshal(blob, event); err != nil {
		return nil, err
	}
	return event, nil
}

// Events returns the events for the action.
// Events are sorted by timestamp.
func (ep *DiskFormat) Events(ctx context.Context, actionID string) ([]*sgevpb.SysGraphEvent, error) {
	allEvents := []*sgevpb.SysGraphEvent{}
	f, err := os.Open(filepath.Join(ep.BasePath, actionID+ep.Format.ext()))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := bufio.NewReader(f)
	for {
		event := &sgevpb.SysGraphEvent{}
		if err := ep.Format.unmarshalMsg(buf, event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		allEvents = append(allEvents, event)
	}
	slices.SortFunc(allEvents, func(a *sgevpb.SysGraphEvent, b *sgevpb.SysGraphEvent) int {
		return a.GetTimestamp().AsTime().Compare(b.GetTimestamp().AsTime())
	})
	return allEvents, nil
}

func (ep *DiskFormat) filepathForEvents(actionID string) string {
	return filepath.Join(ep.BasePath, actionID+ep.Format.ext())
}

func (ep *DiskFormat) filepathForRawEvents(actionID string) string {
	return filepath.Join(ep.BasePath, actionID+sgstorage.RawEventsFileNameSuffix+ep.Format.ext())
}

func appendMsgs[T proto.Message](ctx context.Context, filePath string, fmt EventFileFormat, msgs []T) (int64, error) {
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	totalBytesWritten := int64(0)
	for _, e := range msgs {
		bytesWritten, err := fmt.appendMsg(f, e)
		if err != nil {
			return totalBytesWritten, err
		}
		totalBytesWritten += int64(bytesWritten)
	}
	return totalBytesWritten, nil
}

// WriteEvents writes the events to disk grouped by action ID.
// Returns the total number of bytes written and any error.
//
// There is room for optimization here by writing the events for different actions in parallel but
// that can be done later if this becomes a bottleneck.
func (ep *DiskFormat) WriteEvents(ctx context.Context, events ...*sgevpb.SysGraphEvent) (int64, error) {
	eventsByID := make(map[string][]*sgevpb.SysGraphEvent)
	for _, e := range events {
		eventsByID[e.GetActionId()] = append(eventsByID[e.GetActionId()], e)
	}
	totalBytesWritten := int64(0)
	for actionID, events := range eventsByID {
		bytesWritten, err := appendMsgs(ctx, ep.filepathForEvents(actionID), ep.Format, events)
		if err != nil {
			return totalBytesWritten, err
		}
		totalBytesWritten += bytesWritten
	}
	return totalBytesWritten, nil
}

// WriteRawEvents writes the raw events for a single action ID to disk.
// Returns the total number of bytes written and any error.
func (ep *DiskFormat) WriteRawEvents(ctx context.Context, actionID string, rawEvents ...proto.Message) (int64, error) {
	anys := make([]*anypb.Any, len(rawEvents))
	for i, e := range rawEvents {
		if anyE, ok := e.(*anypb.Any); ok {
			anys[i] = anyE
			continue
		}
		any, err := anypb.New(e)
		if err != nil {
			return 0, err
		}
		anys[i] = any
	}
	return appendMsgs(ctx, ep.filepathForRawEvents(actionID), ep.Format, anys)
}

// LoadToMemory loads the events from disk to memory.
// Returns the InMemoryFormat and any error.
func (ep *DiskFormat) LoadToMemory() (*InMemoryFormat, error) {
	mf := &InMemoryFormat{
		EventMap: make(map[string]*Events),
	}
	// TODO: Pass context to actions call instead of creating a new one.
	ids, err := ep.Actions(context.Background())
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		events, err := ep.Events(context.Background(), id)
		if err != nil {
			return nil, err
		}
		rawEventsCh, rawEventsErrCh, err := ep.RawEvents(context.Background(), id)
		if err != nil {
			return nil, err
		}
		var rawEvents []*anypb.Any
		done := rawEventsCh == nil
		for !done {
			select {
			case rawEvent, ok := <-rawEventsCh:
				if !ok {
					done = true
				} else {
					rawEvents = append(rawEvents, rawEvent)
				}
			case err := <-rawEventsErrCh:
				return nil, err
			}
		}
		select {
		case err := <-rawEventsErrCh:
			return nil, err
		default:
		}
		fmt.Println("Loaded raw events for", id)
		mf.EventMap[id] = &Events{
			Events:    events,
			RawEvents: rawEvents,
		}
	}
	return mf, nil
}

// InMemoryFormat is an IR provider that provides IR events from memory.
type InMemoryFormat struct {
	mu       sync.Mutex
	EventMap map[string]*Events
}

// Events is a struct that contains events and raw events for an action.
type Events struct {
	Events    []*sgevpb.SysGraphEvent
	RawEvents []*anypb.Any
}

// Actions returns the actions in the event provider.
func (ep *InMemoryFormat) Actions(ctx context.Context) ([]string, error) {
	return slices.Collect(maps.Keys(ep.EventMap)), nil
}

// Events returns the events for the action.
func (ep *InMemoryFormat) Events(ctx context.Context, actionID string) ([]*sgevpb.SysGraphEvent, error) {
	if _, ok := ep.EventMap[actionID]; !ok {
		return nil, fmt.Errorf("action %s not found", actionID)
	}
	return ep.EventMap[actionID].Events, nil
}

// RawEvents returns the raw events for the action.
func (ep *InMemoryFormat) RawEvents(ctx context.Context, actionID string) (<-chan *anypb.Any, <-chan error, error) {
	if _, ok := ep.EventMap[actionID]; !ok {
		return nil, nil, fmt.Errorf("action %s not found", actionID)
	}
	rawEvents := ep.EventMap[actionID].RawEvents
	ch := make(chan *anypb.Any, len(rawEvents))
	go func() {
		defer close(ch)
		for _, e := range rawEvents {
			select {
			case <-ctx.Done():
				return
			case ch <- e:
			}
		}
	}()
	return ch, make(<-chan error), nil
}

// WriteEvents writes the events to the in memory EventMap.
// Returns the total number of bytes written and any error.
func (ep *InMemoryFormat) WriteEvents(ctx context.Context, events ...*sgevpb.SysGraphEvent) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.EventMap == nil {
		ep.EventMap = make(map[string]*Events)
	}
	totalSize := int64(0)
	for _, e := range events {
		if _, ok := ep.EventMap[e.GetActionId()]; !ok {
			ep.EventMap[e.GetActionId()] = &Events{}
		}
		ep.EventMap[e.GetActionId()].Events = append(ep.EventMap[e.GetActionId()].Events, e)
		totalSize += int64(proto.Size(e))
	}
	return totalSize, nil
}

// WriteRawEvents writes the raw events for a single action ID to the in memory EventMap.
// Returns the total number of bytes written and any error.
func (ep *InMemoryFormat) WriteRawEvents(ctx context.Context, actionID string, rawEvents ...proto.Message) (int64, error) {
	if len(rawEvents) == 0 {
		return 0, nil
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.EventMap == nil {
		ep.EventMap = make(map[string]*Events)
	}
	if _, ok := ep.EventMap[actionID]; !ok {
		ep.EventMap[actionID] = &Events{}
	}
	var totalSize int64
	for _, e := range rawEvents {
		any, err := anypb.New(e)
		if err != nil {
			return 0, err
		}
		totalSize += int64(proto.Size(any))
		ep.EventMap[actionID].RawEvents = append(ep.EventMap[actionID].RawEvents, any)
	}
	return totalSize, nil
}
