// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgmatch

import (
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func TestActionChainAncestors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	builder := sgstorage.SysGraphBuilder{}
	builder.Action("1").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("1").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/bash", "-c", "echo hello; sleep 10; echo goodbye"}}.Build()
	builder.Action("2").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/tar")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("2").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/tar", "-xzf", "go1.24.1.linux-amd64.tar.gz"}}.Build()
	builder.Action("2").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("3").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/gzip")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/gzip", "-d"}}.Build()
	builder.Action("3").SetParent("2", &sgpb.ActionInteraction{})
	builder.Action("4").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/echo")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/echo", "goodbye"}}.Build()
	builder.Action("4").SetParent("1", &sgpb.ActionInteraction{})
	sg := builder.Build(ctx)

	tests := []struct {
		name       string
		matcher    Edges
		wantChains []Chain
	}{
		{
			name: "AllAncestorsMatch",
			matcher: Edges{
				AllActions(ActionExecutable(ResourcePath("/usr/bin/gzip"))),
				ActionToAllAncestorsTraversal(ActionWithNone(ActionExecutable(ResourcePath("/usr/bin/asdfasdfasdfa")))),
			},
			wantChains: []Chain{{
				Actions: []*sgpb.Action{sg.Actions[3], sg.Actions[2], sg.Actions[1]},
			}},
		},
		{
			name: "AllAncestorsNotMatch",
			matcher: Edges{
				AllActions(ActionExecutable(ResourcePath("/usr/bin/gzip"))),
				ActionToAllAncestorsTraversal(ActionExecutable(ResourcePath("/usr/bin/bash"))),
			},
		},
		{
			name: "AnyAncestorMatch",
			matcher: Edges{
				AllActions(ActionExecutable(ResourcePath("/usr/bin/gzip"))),
				ActionToAnyAncestorTraversal(ActionExecutable(ResourcePath("/usr/bin/bash"))),
			},
			wantChains: []Chain{{
				Actions: []*sgpb.Action{sg.Actions[3], sg.Actions[1]},
			}},
		},
		{
			name: "AnyAncestorNotMatch",
			matcher: Edges{
				AllActions(ActionExecutable(ResourcePath("/usr/bin/gzip"))),
				ActionToAnyAncestorTraversal(ActionExecutable(ResourcePath("/usr/bin/asdfasdfasdfa"))),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chains, err := tc.matcher.AllChains(ctx, sg)
			if err != nil {
				t.Errorf("matcher.AllChains() error = %v, want nil", err)
			}
			if diff := cmp.Diff(tc.wantChains, chains,
				protocmp.Transform(), cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b []*sgpb.Action) bool { return a[len(a)-1].GetId() < b[len(b)-1].GetId() }),
				cmpopts.SortSlices(func(a, b Chain) bool {
					return a.Actions[len(a.Actions)-1].GetId() < b.Actions[len(b.Actions)-1].GetId()
				}),
			); diff != "" {
				t.Errorf("Unexpected diff in chains (-want +got):\n%s", diff)
			}
		})
	}
}

func TestActionChainParentToChild(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	builder := sgstorage.SysGraphBuilder{}
	builder.Action("1").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("1").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/bash", "-c", "echo hello; sleep 10; echo goodbye"}}.Build()
	builder.Action("2").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/echo")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("2").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/echo", "hello"}}.Build()
	builder.Action("2").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("3").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/sleep")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/sleep", "10"}}.Build()
	builder.Action("3").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("4").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/echo")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/echo", "goodbye"}}.Build()
	builder.Action("4").SetParent("1", &sgpb.ActionInteraction{})
	sg := builder.Build(ctx)
	matcher := Edges{
		AllActions(ActionExecutable(ExtractResource("some_key", ResourcePath("/usr/bin/bash")))),
		ParentToChildrenTraversal(ActionExecutable(ResourcePath("/usr/bin/echo"))),
	}
	chains, err := matcher.AllChains(ctx, sg)
	if err != nil {
		t.Errorf("Failed to match chains: %v", err)
	}
	wantChains := []Chain{
		{
			Actions: []*sgpb.Action{
				sg.Actions[1],
				sg.Actions[2],
			},
			Values: map[string][]ExtractedValue{
				"some_key": {
					{
						Resource: sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(),
					},
				},
			},
		},
		{
			Actions: []*sgpb.Action{
				sg.Actions[1],
				sg.Actions[4],
			},
			Values: map[string][]ExtractedValue{
				"some_key": {
					{
						Resource: sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(),
					},
				},
			},
		},
	}
	if diff := cmp.Diff(wantChains, chains,
		protocmp.Transform(), cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b []*sgpb.Action) bool { return a[len(a)-1].GetId() < b[len(b)-1].GetId() }),
		cmpopts.SortSlices(func(a, b Chain) bool {
			return a.Actions[len(a.Actions)-1].GetId() < b.Actions[len(b.Actions)-1].GetId()
		}),
	); diff != "" {
		t.Errorf("Unexpected diff in chains (-want +got):\n%s", diff)
	}
}

func TestActionChainOutputToInput(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	builder := sgstorage.SysGraphBuilder{}
	builder.Action("1").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("1").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/bash", "-c", "/usr/bin/another; sleep 10; cat a.txt"}}.Build()
	builder.Action("2").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/another")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("2").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/another"}}.Build()
	builder.Action("2").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("2").AddOutput(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("a.txt")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/sleep")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/sleep", "10"}}.Build()
	builder.Action("3").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("4").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/cat")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/cat", "a.txt"}}.Build()
	builder.Action("4").AddInput(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("a.txt")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").SetParent("1", &sgpb.ActionInteraction{})
	sg := builder.Build(ctx)
	matcher := Edges{
		AllActions(ActionExecutable(ResourcePath("/usr/bin/another"))),
		OutputToInputTraversal(ActionExecutable(ResourcePath("/usr/bin/cat")), ResourcePathSuffix(".txt")),
	}
	chains, err := matcher.AllChains(ctx, sg)
	if err != nil {
		t.Errorf("Failed to match chains: %v", err)
	}
	wantChains := []Chain{
		{
			Actions: []*sgpb.Action{
				sg.Actions[2],
				sg.Actions[4],
			},
		},
	}
	if diff := cmp.Diff(wantChains, chains, protocmp.Transform(), cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b []*sgpb.Action) bool { return a[len(a)-1].GetId() < b[len(b)-1].GetId() })); diff != "" {
		t.Errorf("Unexpected diff in chains (-want +got):\n%s", diff)
	}
}

func TestInputToProducerTraversal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	builder := sgstorage.SysGraphBuilder{}
	builder.Action("1").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("1").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/bash", "-c", "/usr/bin/another; sleep 10; cat a.txt"}}.Build()
	builder.Action("2").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/another")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("2").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/another"}}.Build()
	builder.Action("2").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("2").AddOutput(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("a.txt")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/sleep")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/sleep", "10"}}.Build()
	builder.Action("3").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("4").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/cat")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/cat", "a.txt"}}.Build()
	builder.Action("4").AddInput(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("a.txt")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("4").SetParent("1", &sgpb.ActionInteraction{})
	sg := builder.Build(ctx)
	matcher := Edges{
		AllActions(ActionExecutable(ResourcePath("/usr/bin/cat"))),
		InputToProducerTraversal(ActionExecutable(ResourcePath("/usr/bin/another")), ResourcePathSuffix(".txt")),
	}
	chains, err := matcher.AllChains(ctx, sg)
	if err != nil {
		t.Errorf("Failed to match chains: %v", err)
	}
	wantChains := []Chain{
		{
			Actions: []*sgpb.Action{
				sg.Actions[4],
				sg.Actions[2],
			},
		},
	}
	if diff := cmp.Diff(wantChains, chains, protocmp.Transform(), cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b []*sgpb.Action) bool { return a[len(a)-1].GetId() < b[len(b)-1].GetId() })); diff != "" {
		t.Errorf("Unexpected diff in chains (-want +got):\n%s", diff)
	}
}

func TestUnproducedResource(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	builder := sgstorage.SysGraphBuilder{}
	builder.Action("1").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/bash")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("1").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/bash", "-c", "/usr/bin/another; sleep 10; cat a.txt"}}.Build()
	builder.Action("2").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/sleep")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("2").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/sleep", "10"}}.Build()
	builder.Action("2").SetParent("1", &sgpb.ActionInteraction{})
	builder.Action("3").SetExecutable(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/cat")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").ExecInfo = sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/cat", "a.txt"}}.Build()
	builder.Action("3").AddInput(sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("a.txt")}.Build()}.Build(), &sgpb.ResourceInteraction{})
	builder.Action("3").SetParent("1", &sgpb.ActionInteraction{})
	sg := builder.Build(ctx)
	matcher := Edges{
		AllActions(ActionExecutable(ResourcePath("/usr/bin/cat"))),
		UnproducedResource(ResourcePathSuffix(".txt")),
	}
	chains, err := matcher.AllChains(ctx, sg)
	if err != nil {
		t.Errorf("Failed to match chains: %v", err)
	}
	wantChains := []Chain{
		{
			Actions: []*sgpb.Action{
				sg.Actions[3],
			},
		},
	}
	if diff := cmp.Diff(wantChains, chains, protocmp.Transform(), cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b []*sgpb.Action) bool { return a[len(a)-1].GetId() < b[len(b)-1].GetId() })); diff != "" {
		t.Errorf("Unexpected diff in chains (-want +got):\n%s", diff)
	}
}

func TestActionMatcher(t *testing.T) {
	res1 := sgpb.Resource_builder{FileInfo: sgpb.FileInfo_builder{Path: proto.String("/usr/bin/executable")}.Build()}.Build()
	tests := []struct {
		name      string
		sg        *inmemory.SysGraph
		action    *sgpb.Action
		matcher   Action
		wantMatch bool
	}{
		{
			name: "ActionExecutableMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePath("/usr/bin/executable")),
			wantMatch: true,
		},
		{
			name: "ActionExecutableNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePath("/usr/bin/other")),
			wantMatch: false,
		},

		{
			name: "ActionExecutableSuffixMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePathSuffix("/executable")),
			wantMatch: true,
		},
		{
			name: "ActionExecutableSuffixNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePathSuffix("/other")),
			wantMatch: false,
		},
		{
			name: "ActionExecutableRegexpMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePathRegexp(regexp.MustCompile(".*/executable$"))),
			wantMatch: true,
		},
		{
			name: "ActionExecutableRegexpNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourcePathRegexp(regexp.MustCompile(".*/other$"))),
			wantMatch: false,
		},
		{
			name: "ActionExecutableResourceWithAllMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourceWithAll(ResourcePathPrefix("/usr/bin/"), ResourcePathSuffix("/executable"))),
			wantMatch: true,
		},
		{
			name: "ActionExecutableResourceWithAllNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourceWithAll(ResourcePathPrefix("/usr/bin/"), ResourcePathSuffix("/other"))),
			wantMatch: false,
		},
		{
			name: "ActionExecutableResourceWithNoneMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourceWithNone(ResourcePathPrefix("/usr/sbin/"), ResourcePathSuffix("/other"))),
			wantMatch: true,
		},
		{
			name: "ActionExecutableResourceWithNoneNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher:   ActionExecutable(ResourceWithNone(ResourcePathPrefix("/usr/bin/"), ResourcePathSuffix("/other"))),
			wantMatch: false,
		},
		{
			name: "ActionArgvMatch",
			action: sgpb.Action_builder{
				ExecInfo: sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build(),
			}.Build(),
			matcher:   ActionArgv([]string{"/usr/bin/executable", "arg1", "arg2"}),
			wantMatch: true,
		},
		{
			name: "ActionArgvNoMatch",
			action: sgpb.Action_builder{
				ExecInfo: sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build(),
			}.Build(),
			matcher:   ActionArgv([]string{"/usr/bin/executable", "arg1", "arg3"}),
			wantMatch: false,
		},
		{
			name: "ActionOutputMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				Outputs: map[string]*sgpb.ResourceInteractions{
					must(pbdigest.NewFromMessage(res1)).String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
			matcher:   ActionOutput(ResourcePath("/usr/bin/executable")),
			wantMatch: true,
		},
		{
			name: "ActionOutputNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				Outputs: map[string]*sgpb.ResourceInteractions{
					must(pbdigest.NewFromMessage(res1)).String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
			matcher:   ActionOutput(ResourcePath("/usr/bin/other")),
			wantMatch: false,
		},
		{
			name: "ActionInputMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				Inputs: map[string]*sgpb.ResourceInteractions{
					must(pbdigest.NewFromMessage(res1)).String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
			matcher:   ActionInput(ResourcePath("/usr/bin/executable")),
			wantMatch: true,
		},
		{
			name: "ActionInputNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				Outputs: map[string]*sgpb.ResourceInteractions{
					must(pbdigest.NewFromMessage(res1)).String(): sgpb.ResourceInteractions_builder{}.Build(),
				},
			}.Build(),
			matcher:   ActionInput(ResourcePath("/usr/bin/other")),
			wantMatch: false,
		},
		{
			name: "ActionExitSignalMatch",
			action: sgpb.Action_builder{
				ExitSignal: proto.String("SIGTERM"),
			}.Build(),
			matcher:   ActionExitSignal("SIGTERM"),
			wantMatch: true,
		},
		{
			name: "ActionExitSignalNoMatch",
			action: sgpb.Action_builder{
				ExitSignal: proto.String("SIGKILL"),
			}.Build(),
			matcher:   ActionExitSignal("SIGTERM"),
			wantMatch: false,
		},
		{
			name: "ActionExitStatusMatch",
			action: sgpb.Action_builder{
				ExitStatus: proto.Uint32(1),
			}.Build(),
			matcher:   ActionExitStatus(1),
			wantMatch: true,
		},
		{
			name: "ActionExitStatusNoMatch",
			action: sgpb.Action_builder{
				ExitStatus: proto.Uint32(1),
			}.Build(),
			matcher:   ActionExitStatus(0),
			wantMatch: false,
		},
		{
			name: "ActionWithAllMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
				ExecInfo:                 sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build()}.Build(),
			matcher: ActionWithAll(
				ActionExecutable(ResourcePath("/usr/bin/executable")),
				ActionArgv([]string{"/usr/bin/executable", "arg1", "arg2"}),
			),
			wantMatch: true,
		},
		{
			name: "ActionWithAllOnlyPartOfMatchersMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher: ActionWithAll(
				ActionExecutable(ResourcePath("/usr/bin/executable")),
				ActionArgv([]string{"/usr/bin/executable", "arg1", "arg2"}),
			),
			wantMatch: false,
		},
		{
			name: "ActionWithAllNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
				ExecInfo:                 sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build()}.Build(),
			matcher: ActionWithAll(
				ActionExecutable(ResourcePath("/usr/bin/other")),
				ActionArgv([]string{"/usr/bin/other", "arg1", "arg2"}),
			),
			wantMatch: false,
		},

		{
			name: "ActionWithNoneMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
				ExecInfo:                 sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build()}.Build(),
			matcher: ActionWithNone(
				ActionExecutable(ResourcePath("/usr/bin/executable")),
				ActionArgv([]string{"/usr/bin/executable", "arg1", "arg2"}),
			),
			wantMatch: false,
		},
		{
			name: "ActionWithNoneOnlyPartOfMatchersMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
			}.Build(),
			matcher: ActionWithNone(
				ActionExecutable(ResourcePath("/usr/bin/executable")),
				ActionArgv([]string{"/usr/bin/executable", "arg1", "arg2"}),
			),
			wantMatch: false,
		},
		{
			name: "ActionWithNoneNoMatch",
			sg: &inmemory.SysGraph{
				ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
					must(pbdigest.NewFromMessage(res1)): res1,
				},
			},
			action: sgpb.Action_builder{
				ExecutableResourceDigest: proto.String(must(pbdigest.NewFromMessage(res1)).String()),
				ExecInfo:                 sgpb.ExecInfo_builder{Argv: []string{"/usr/bin/executable", "arg1", "arg2"}}.Build()}.Build(),
			matcher: ActionWithNone(
				ActionExecutable(ResourcePath("/usr/bin/other")),
				ActionArgv([]string{"/usr/bin/other", "arg1", "arg2"}),
			),
			wantMatch: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotMatch, _, err := tc.matcher.Matches(t.Context(), tc.sg, tc.action)
			if err != nil {
				t.Fatalf("Error matching action: %v", err)
			}
			if gotMatch != tc.wantMatch {
				t.Errorf("Unexpected match result for action %v, got %v, want %v", tc.action, gotMatch, tc.wantMatch)
			}
		})
	}
}
