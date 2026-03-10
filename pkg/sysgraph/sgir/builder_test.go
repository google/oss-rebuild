// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgir

import (
	"testing"
	"time"

	evtpb "github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	anypb "google.golang.org/protobuf/types/known/anypb"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

var (
	fileRes = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/10"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	file2Res = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file2"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/100"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	file3Res = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file3"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/103"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	file4Res = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_FILE.Enum(),
		FileInfo: sgpb.FileInfo_builder{
			Path:   proto.String("path/to/file4"),
			Digest: proto.String("1234567890123456789012345678901234567890123456789012345678901234/104"),
			Type:   sgpb.FileType_FILE_TYPE_REGULAR.Enum(),
		}.Build(),
	}.Build()
	tcpRes = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS.Enum(),
		NetworkAddrInfo: sgpb.NetworkAddrInfo_builder{
			Protocol: proto.String("tcp"),
			Address:  proto.String("127.0.0.1:8080"),
		}.Build(),
	}.Build()

	pipeRes = sgpb.Resource_builder{
		Type: sgpb.ResourceType_RESOURCE_TYPE_PIPE.Enum(),
		PipeInfo: sgpb.PipeInfo_builder{
			ReadEnd: sgpb.StdIODupInfo_builder{
				OldFd: proto.Int32(3),
				NewFd: proto.Int32(0),
			}.Build(),
			ReadExecId: proto.String("action4"),
			WriteEnd: sgpb.StdIODupInfo_builder{
				OldFd: proto.Int32(4),
				NewFd: proto.Int32(1),
			}.Build(),
			WriteExecId: proto.String("action3"),
		}.Build(),
	}.Build()
)

func mustAny(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	any, err := anypb.New(m)
	if err != nil {
		t.Fatalf("anypb.New(%q) failed: %v", m, err)
	}
	return any
}
func mustDigest(t *testing.T, m proto.Message) pbdigest.Digest {
	t.Helper()
	dg, err := pbdigest.NewFromMessage(m)
	if err != nil {
		t.Fatalf("pbdigest.NewFromMessage(%q) failed: %v", m, err)
	}
	return dg
}
func TestConstructSysGraph(t *testing.T) {
	tests := []struct {
		name           string
		storeRawEvents bool
	}{
		{
			name:           "store_raw_events",
			storeRawEvents: true,
		},
		{
			name:           "no_store_raw_events",
			storeRawEvents: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action1 := "action1"
			action2 := "action2"
			// action1 (risky pipe) has 4 dup action as direct children:
			//  * action3 (write end of the risky pipe),
			//  * action4 (read end of the risky pipe),
			//  * action5 (a non risky dup action, say from echo hello > foo.txt)
			// we should report a pipe resource is used for IPC between action3 and action4 in sysgraph.
			action3 := "action3"
			action4 := "action4"
			action5 := "action5"
			// action6 (non risky pipe) has only 1 dup action as direct child:
			//  * action7 (say, a dup(3, 0) from xargs bash -c < output.txt
			// we don't present the non-risky pipe as a resource in sysgraph.
			action6 := "action6"
			action7 := "action7"
			events := &InMemoryFormat{
				EventMap: map[string]*Events{
					action1: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(1, 1)),
								StartEvent: sgpb.StartEvent_builder{
									Timestamp: tpb.New(time.Unix(1, 1)),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(2, 2)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action2),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action3),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(2, 2)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action4),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(5, 5)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action5),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(6, 6)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action6),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(3, 3)),
								MetadataEvent: sgpb.MetadataEvent_builder{
									Key:   proto.String("key1"),
									Value: proto.String("value1"),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ResourceEvent: sgpb.ResourceEvent_builder{
									Resource:  fileRes,
									EventType: sgpb.ResourceEvent_EVENT_TYPE_INPUT.Enum(),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ResourceEvent: sgpb.ResourceEvent_builder{
									Resource:  file3Res,
									EventType: sgpb.ResourceEvent_EVENT_TYPE_DELETE.Enum(),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(4, 4)),
								PipeEvent: sgpb.PipeEvent_builder{}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action1),
								Timestamp: tpb.New(time.Unix(1, 1)),
								EndEvent: sgpb.EndEvent_builder{
									Timestamp: tpb.New(time.Unix(20, 20)),
									Status:    proto.Uint32(0),
									Signal:    proto.String(""),
								}.Build(),
							}.Build(),
						},
					},
					action2: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action2),
								Timestamp: tpb.New(time.Unix(2, 2)),
								StartEvent: sgpb.StartEvent_builder{
									Timestamp: tpb.New(time.Unix(2, 2)),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action2),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ResourceEvent: sgpb.ResourceEvent_builder{
									Resource:  file2Res,
									EventType: sgpb.ResourceEvent_EVENT_TYPE_OUTPUT.Enum(),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action2),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ResourceEvent: sgpb.ResourceEvent_builder{
									Resource:          file3Res,
									EventType:         sgpb.ResourceEvent_EVENT_TYPE_RENAME_SOURCE.Enum(),
									RenamePartnerPath: proto.String(file4Res.GetFileInfo().GetPath()),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action2),
								Timestamp: tpb.New(time.Unix(3, 3)),
								ResourceEvent: sgpb.ResourceEvent_builder{
									Resource:          file4Res,
									EventType:         sgpb.ResourceEvent_EVENT_TYPE_RENAME_DEST.Enum(),
									RenamePartnerPath: proto.String(file3Res.GetFileInfo().GetPath()),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action2),
								Timestamp: tpb.New(time.Unix(2, 2)),
								EndEvent: sgpb.EndEvent_builder{
									Timestamp: tpb.New(time.Unix(10, 10)),
									Status:    proto.Uint32(1),
									Signal:    proto.String("SIGKILL"),
								}.Build(),
							}.Build(),
						},
						RawEvents: []*anypb.Any{mustAny(t,
							&evtpb.GetEventsResponse{
								Time: tpb.New(time.Unix(2, 2)),
							}),
						},
					},
					action3: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action3),
								Timestamp: tpb.New(time.Unix(3, 3)),
								DupEvent: sgpb.DupEvent_builder{
									OldFd:        proto.Int32(4),
									NewFd:        proto.Int32(1),
									ParentExecId: proto.String(action1),
									Timestamp:    tpb.New(time.Unix(3, 3)),
								}.Build(),
							}.Build(),
						},
					},
					action4: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action4),
								Timestamp: tpb.New(time.Unix(2, 2)),
								DupEvent: sgpb.DupEvent_builder{
									OldFd:        proto.Int32(3),
									NewFd:        proto.Int32(0),
									ParentExecId: proto.String(action1),
									// Note that regardless of the action's timestamp, all dup events from the same
									// parent action are sorted by the dup event's timestamp.
									Timestamp: tpb.New(time.Unix(4, 4)),
								}.Build(),
							}.Build(),
						}},
					action5: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action5),
								Timestamp: tpb.New(time.Unix(5, 5)),
								DupEvent: sgpb.DupEvent_builder{
									OldFd:        proto.Int32(5),
									NewFd:        proto.Int32(0),
									ParentExecId: proto.String(action1),
									Timestamp:    tpb.New(time.Unix(5, 5)),
								}.Build(),
							}.Build(),
						}},
					action6: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action6),
								Timestamp: tpb.New(time.Unix(6, 6)),
								StartEvent: sgpb.StartEvent_builder{
									Timestamp: tpb.New(time.Unix(6, 6)),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action6),
								Timestamp: tpb.New(time.Unix(7, 7)),
								ChildEvent: sgpb.ChildEvent_builder{
									ChildActionId: proto.String(action7),
								}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action6),
								Timestamp: tpb.New(time.Unix(7, 7)),
								PipeEvent: sgpb.PipeEvent_builder{}.Build(),
							}.Build(),
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action6),
								Timestamp: tpb.New(time.Unix(6, 6)),
								EndEvent: sgpb.EndEvent_builder{
									Timestamp: tpb.New(time.Unix(10, 10)),
									Status:    proto.Uint32(0),
									Signal:    proto.String(""),
								}.Build(),
							}.Build(),
						}},
					action7: {
						Events: []*sgpb.SysGraphEvent{
							sgpb.SysGraphEvent_builder{
								ActionId:  proto.String(action7),
								Timestamp: tpb.New(time.Unix(7, 7)),
								DupEvent: sgpb.DupEvent_builder{
									OldFd:        proto.Int32(3),
									NewFd:        proto.Int32(0),
									ParentExecId: proto.String(action6),
								}.Build(),
							}.Build(),
						}},
				},
			}
			constructor := &Builder{
				ConcurrencyLimit: 10,
				StoreRawEvents:   tc.storeRawEvents,
			}
			sgDir := t.TempDir()
			if err := constructor.ToSysGraph(t.Context(), "graphID", events, sgDir); err != nil {
				t.Errorf("ConstructSysGraph returned unexpected error: %v", err)
			}
			sg, err := sgstorage.LoadSysGraph(t.Context(), sgDir)
			if err != nil {
				t.Errorf("sgstorage.LoadSysGraph() returned unexpected error for generated sysgraph: %v", err)
			}
			got, err := sgtransform.Load(t.Context(), sg)
			if err != nil {
				t.Errorf("sgtranform.Load() returned unexpected error for generated sysgraph: %v", err)
			}
			wantEvents := map[int64][]*anypb.Any{}
			if tc.storeRawEvents {
				a, _ := anypb.New(&evtpb.GetEventsResponse{
					Time: tpb.New(time.Unix(2, 2)),
				})
				wantEvents[2] = []*anypb.Any{a}
			}
			want := &inmemory.SysGraph{
				GraphPb: sgpb.SysGraph_builder{
					Id:                  proto.String("graphID"),
					EntryPointActionIds: []int64{1},
				}.Build(),
				Actions: map[int64]*sgpb.Action{
					1: sgpb.Action_builder{
						Id:         proto.Int64(1),
						SysGraphId: proto.String("graphID"),
						StartTime:  &tpb.Timestamp{Seconds: 1, Nanos: 1},
						EndTime:    &tpb.Timestamp{Seconds: 20, Nanos: 20},
						Children: map[int64]*sgpb.ActionInteraction{
							2: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
							}.Build(),
							3: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
							}.Build(),
							4: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
							}.Build(),
							5: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 5, Nanos: 5},
							}.Build(),
							6: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 6, Nanos: 6},
							}.Build(),
						},
						Inputs: map[string]*sgpb.ResourceInteractions{
							mustDigest(t, fileRes).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
										Type:      sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_READ.Enum(),
									}.Build(),
								},
							}.Build(),
						},
						Outputs: map[string]*sgpb.ResourceInteractions{
							mustDigest(t, file3Res).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
										Type:      sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_DELETE.Enum(),
									}.Build(),
								},
							}.Build(),
						},
						Metadata: map[string]string{
							"key1":       "value1",
							"risky_pipe": "true",
						},
						ExitStatus: proto.Uint32(0),
						ExitSignal: proto.String(""),
					}.Build(),
					2: sgpb.Action_builder{
						Id:             proto.Int64(2),
						SysGraphId:     proto.String("graphID"),
						ParentActionId: proto.Int64(1),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
						}.Build(),
						StartTime: &tpb.Timestamp{Seconds: 2, Nanos: 2},
						EndTime:   &tpb.Timestamp{Seconds: 10, Nanos: 10},
						Outputs: map[string]*sgpb.ResourceInteractions{
							mustDigest(t, file2Res).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
										Type:      sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_WRITE.Enum(),
									}.Build(),
								},
							}.Build(),
							mustDigest(t, file3Res).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp:           &tpb.Timestamp{Seconds: 3, Nanos: 3},
										Type:                sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_RENAME_SOURCE.Enum(),
										RenamePartnerDigest: proto.String(mustDigest(t, file4Res).String()),
									}.Build(),
								},
							}.Build(),
							mustDigest(t, file4Res).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp:           &tpb.Timestamp{Seconds: 3, Nanos: 3},
										Type:                sgpb.ResourceInteractionType_RESOURCE_INTERACTION_TYPE_RENAME_DESTINATION.Enum(),
										RenamePartnerDigest: proto.String(mustDigest(t, file3Res).String()),
									}.Build(),
								},
							}.Build(),
						},
						ExitStatus: proto.Uint32(1),
						ExitSignal: proto.String("SIGKILL"),
					}.Build(),
					// The write end of the pipe: dup2(4, 1)
					3: sgpb.Action_builder{
						Id:             proto.Int64(3),
						SysGraphId:     proto.String("graphID"),
						ParentActionId: proto.Int64(1),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
						}.Build(),
						Outputs: map[string]*sgpb.ResourceInteractions{
							mustDigest(t, pipeRes).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp: &tpb.Timestamp{Seconds: 3, Nanos: 3},
									}.Build(),
								},
							}.Build(),
						},
					}.Build(),
					// The read end of the pipe: dup2(3, 0)
					4: sgpb.Action_builder{
						Id:             proto.Int64(4),
						SysGraphId:     proto.String("graphID"),
						ParentActionId: proto.Int64(1),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
						}.Build(),
						Inputs: map[string]*sgpb.ResourceInteractions{
							mustDigest(t, pipeRes).String(): sgpb.ResourceInteractions_builder{
								Interactions: []*sgpb.ResourceInteraction{
									sgpb.ResourceInteraction_builder{
										Timestamp: &tpb.Timestamp{Seconds: 2, Nanos: 2},
									}.Build(),
								},
							}.Build(),
						},
					}.Build(),
					// An extra dup action, which is not risky as it is not communicating with other child actions.
					5: sgpb.Action_builder{
						Id:             proto.Int64(5),
						SysGraphId:     proto.String("graphID"),
						ParentActionId: proto.Int64(1),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 5, Nanos: 5},
						}.Build(),
					}.Build(),
					6: sgpb.Action_builder{
						Id:             proto.Int64(6),
						SysGraphId:     proto.String("graphID"),
						StartTime:      &tpb.Timestamp{Seconds: 6, Nanos: 6},
						EndTime:        &tpb.Timestamp{Seconds: 10, Nanos: 10},
						ParentActionId: proto.Int64(1),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 6, Nanos: 6},
						}.Build(),
						Children: map[int64]*sgpb.ActionInteraction{
							7: sgpb.ActionInteraction_builder{
								Timestamp: &tpb.Timestamp{Seconds: 7, Nanos: 7},
							}.Build(),
						},
						ExitStatus: proto.Uint32(0),
						ExitSignal: proto.String(""),
					}.Build(),
					7: sgpb.Action_builder{
						Id:             proto.Int64(7),
						SysGraphId:     proto.String("graphID"),
						ParentActionId: proto.Int64(6),
						Parent: sgpb.ActionInteraction_builder{
							Timestamp: &tpb.Timestamp{Seconds: 7, Nanos: 7},
						}.Build(),
					}.Build(),
				},
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					mustDigest(t, fileRes):  fileRes,
					mustDigest(t, file2Res): file2Res,
					mustDigest(t, file3Res): file3Res,
					mustDigest(t, file4Res): file4Res,
					mustDigest(t, pipeRes):  pipeRes,
				},
				Events: wantEvents,
			}
			if diff := cmp.Diff(want, got, protocmp.Transform(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("sysgraph differs from expected, diff %s", diff)
				t.Logf("Got: %v", got.Events)
				t.Logf("Expected: %v", want.Events)
			}
		})
	}
}
