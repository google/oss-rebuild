// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgquery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/sysgraph/inmemory"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

func TestRangeParallel(t *testing.T) {
	t.Parallel()
	inputs := []int{1, 2, 3, 4, 5}
	var m sync.Map
	err := rangeParallel(context.Background(), inputs, func(ctx context.Context, i int) error {
		m.Store(i, true)
		return nil
	})
	if err != nil {
		t.Errorf("rangeParallel returned unexpected error: %v", err)
	}
	var got []int
	m.Range(func(k, v any) bool {
		got = append(got, k.(int))
		return true
	})
	if diff := cmp.Diff(got, inputs, cmpopts.SortSlices(func(a, b int) bool { return a < b })); diff != "" {
		t.Errorf("rangeParallel retued unexpected results, diff\n%s", diff)
	}
}

func TestRangeParallelError(t *testing.T) {
	t.Parallel()
	inputs := []int{1, 2, 3, 4, 5}
	var m sync.Map
	wantErr := fmt.Errorf("error")
	err := rangeParallel(context.Background(), inputs, func(ctx context.Context, i int) error {
		m.Store(i, true)
		if i == 3 {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("rangeParallel returned unexpected error: %v", err)
	}
}

func TestMapParallel(t *testing.T) {
	t.Parallel()
	inputs := []int{1, 2, 3, 4, 5}
	want := []int{1 * 10, 2 * 10, 3 * 10, 4 * 10, 5 * 10}
	got, err := mapParallel(context.Background(), inputs, func(ctx context.Context, i int) (int, error) {
		return i * 10, nil
	})
	if err != nil {
		t.Errorf("mapParallel returned unexpected error: %v", err)
	}
	if diff := cmp.Diff(got, want, cmpopts.SortSlices(func(a, b int) bool { return a < b })); diff != "" {
		t.Errorf("mapParallel returned unexpected results, diff\n%s", diff)
	}
}

func TestMapParallelCancel(t *testing.T) {
	t.Parallel()
	inputs := []int{1, 2, 3, 4, 5}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := mapParallel(ctx, inputs, func(ctx context.Context, i int) (int, error) {
		select {
		case <-ctx.Done():
			// Return a value to ensure we are testing mapParallel's cancellation behavior.
			return i * 10, nil
		default:
			cancel()
			return i * 10, nil
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("mapParallel returned unexpected error: %v", err)
	}
}

func TestMapParallelError(t *testing.T) {
	t.Parallel()
	inputs := []int{1, 2, 3, 4, 5}
	wantErr := fmt.Errorf("error")
	_, err := mapParallel(context.Background(), inputs, func(ctx context.Context, i int) (int, error) {
		if i == 3 {
			return 0, wantErr
		}
		return i * 10, nil
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("mapParallel returned unexpected error: %v", err)
	}
}

func TestMapAllActions(t *testing.T) {
	t.Parallel()
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
			2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
			3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
			4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
			5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
		},
	}
	got, err := MapAllActions(context.Background(), sg, func(a *sgpb.Action) (int64, int64, error) {
		return a.GetId(), a.GetId() * 10, nil
	})
	if err != nil {
		t.Errorf("MapAllActions returned unexpected error: %v", err)
	}
	want := map[int64]int64{
		1: 10,
		2: 20,
		3: 30,
		4: 40,
		5: 50,
	}
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("MapAllActions returned unexpected results, diff\n%s", diff)
	}
}

func TestMapAllActionsError(t *testing.T) {
	t.Parallel()
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
			2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
			3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
			4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
			5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
		},
	}
	wantErr := fmt.Errorf("error")
	_, err := MapAllActions(context.Background(), sg, func(a *sgpb.Action) (int64, int64, error) {
		if a.GetId() == 3 {
			return 0, 0, wantErr
		}
		return a.GetId(), a.GetId() * 10, nil
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("MapAllActions returned unexpected error: %v", err)
	}
}

func TestFilterActions(t *testing.T) {
	t.Parallel()
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
			2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
			3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
			4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
			5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
		},
	}
	got, err := FilterActions(context.Background(), sg, func(_ context.Context, _ *inmemory.SysGraph, a *sgpb.Action) bool {
		return a.GetId()%2 == 0
	})
	if err != nil {
		t.Errorf("FilterActions returned unexpected error: %v", err)
	}
	want := []*sgpb.Action{
		sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
		sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
	}
	if diff := cmp.Diff(got, want, protocmp.Transform(), cmpopts.SortSlices(func(a, b *sgpb.Action) bool { return a.GetId() < b.GetId() })); diff != "" {
		t.Errorf("FilterActions returned unexpected results, diff\n%s", diff)
	}
}

func TestRangeActions(t *testing.T) {
	t.Parallel()
	var m sync.Map
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
			2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
			3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
			4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
			5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
		},
	}
	err := RangeActions(context.Background(), sg, func(ctx context.Context, a *sgpb.Action) error {
		m.Store(a.GetId(), true)
		return nil
	})
	if err != nil {
		t.Errorf("RangeActions returned unexpected error: %v", err)
	}
	var got []int64
	m.Range(func(k, v any) bool {
		got = append(got, k.(int64))
		return true
	})
	want := []int64{1, 2, 3, 4, 5}
	if diff := cmp.Diff(got, want, cmpopts.SortSlices(func(a, b int64) bool { return a < b })); diff != "" {
		t.Errorf("RangeActions returned unexpected results, diff\n%s", diff)
	}
}

func TestRangeActionsError(t *testing.T) {
	t.Parallel()
	var m sync.Map
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{Id: proto.Int64(1)}.Build(),
			2: sgpb.Action_builder{Id: proto.Int64(2)}.Build(),
			3: sgpb.Action_builder{Id: proto.Int64(3)}.Build(),
			4: sgpb.Action_builder{Id: proto.Int64(4)}.Build(),
			5: sgpb.Action_builder{Id: proto.Int64(5)}.Build(),
		},
	}
	wantErr := fmt.Errorf("error")
	err := RangeActions(context.Background(), sg, func(ctx context.Context, a *sgpb.Action) error {
		m.Store(a.GetId(), true)
		if a.GetId() == 3 {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("RangeActions returned unexpected error: %v", err)
	}
}

// This test simulates the following scenario:
// 1. Action 1 inputs resource 1 at time t1.
// 2. Action 2 outputs resource 1 at time t2.
// 3. Action 3 inputs resource 1 at time t3.
// 4. Action 4 outputs resource 1 at time t4.
// 4. Action 4 outputs resource 2 at time t5.
// 5. Action 5 inputs resource 1 at time t6.
// 6. Action 6 inputs resource 2 at time t6.
// 7. Action 6 inputs resource 3 at time t7.
//
// We expect:
// 1. Action 1 has no resource dependencies.
// 2. Action 2 has no resource dependencies.
// 3. Action 3 has a resource dependency on action 2.
// 4. Action 4 has no resource dependencies.
// 5. Action 5 has a resource dependency on action 2 and action 4.
// 6. Action 6 has a resource dependency on action 4.
func TestResourceDependencies(t *testing.T) {
	t.Parallel()
	base := time.Date(2024, 05, 01, 0, 0, 0, 0, time.UTC)
	t1 := tpb.New(base)
	t2 := tpb.New(base.Add(time.Minute))
	t3 := tpb.New(base.Add(time.Minute * 2))
	t4 := tpb.New(base.Add(time.Minute * 3))
	t5 := tpb.New(base.Add(time.Minute * 4))
	t6 := tpb.New(base.Add(time.Minute * 5))
	sg := &inmemory.SysGraph{
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Inputs: map[string]*sgpb.ResourceInteractions{
					"1": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t1}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id: proto.Int64(2),
				Outputs: map[string]*sgpb.ResourceInteractions{
					"1": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t2}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id: proto.Int64(3),
				Inputs: map[string]*sgpb.ResourceInteractions{
					"1": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t3}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id: proto.Int64(4),
				Outputs: map[string]*sgpb.ResourceInteractions{
					"1": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t4}.Build(),
						},
					}.Build(),
					"2": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t5}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			5: sgpb.Action_builder{
				Id: proto.Int64(5),
				Inputs: map[string]*sgpb.ResourceInteractions{
					"1": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t6}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			6: sgpb.Action_builder{
				Id: proto.Int64(6),
				Inputs: map[string]*sgpb.ResourceInteractions{
					"2": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t6}.Build(),
						},
					}.Build(),
					"3": sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t6}.Build(),
						},
					}.Build(),
				},
			}.Build(),
		},
	}
	got, err := ResourceDependencies(context.Background(), sg, 1)
	if err != nil {
		t.Errorf("ResourceDependencies returned unexpected error: %v", err)
	}
	want := map[int64][]int64{
		3: {2},
		5: {2, 4},
		6: {4},
	}
	if diff := cmp.Diff(got, want, cmpopts.SortSlices(func(a, b int64) bool { return a < b })); diff != "" {
		t.Errorf("ResourceDependencies returned unexpected results, diff\n%s", diff)
	}
}

// Action 1 is a risky pipe action.
// Action 2 is the read end of the pipe
// Action 3 is the write end of the pipe.
// Action 4 is a half pipe, we don't report it in sysgraph yet.
func TestRiskyPipes(t *testing.T) {
	t.Parallel()
	base := time.Date(2024, 05, 01, 0, 0, 0, 0, time.UTC)
	t2 := tpb.New(base.Add(time.Minute))
	t3 := tpb.New(base.Add(time.Minute * 2))
	t4 := tpb.New(base.Add(time.Minute * 3))
	fullPipeDigestStr := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456700/123"
	halfPipeDigestStr := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456700/456"

	fullPipeDigest, err := pbdigest.NewFromString(fullPipeDigestStr)
	if err != nil {
		t.Fatalf("Failed to create pipe digest: %v", err)
	}
	halfPipeDigest, err := pbdigest.NewFromString(halfPipeDigestStr)
	if err != nil {
		t.Fatalf("Failed to create pipe digest: %v", err)
	}
	sg := &inmemory.SysGraph{
		ResourceMap: map[pbdigest.Digest]*sgpb.Resource{
			fullPipeDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_PIPE.Enum(),
			}.Build(),
			halfPipeDigest: sgpb.Resource_builder{
				Type: sgpb.ResourceType_RESOURCE_TYPE_PIPE.Enum(),
			}.Build(),
		},
		Actions: map[int64]*sgpb.Action{
			1: sgpb.Action_builder{
				Id: proto.Int64(1),
				Metadata: map[string]string{
					"risky_pipe": "true",
				},
				Children: map[int64]*sgpb.ActionInteraction{
					2: sgpb.ActionInteraction_builder{Timestamp: t2}.Build(),
					3: sgpb.ActionInteraction_builder{Timestamp: t3}.Build(),
					4: sgpb.ActionInteraction_builder{Timestamp: t4}.Build(),
				},
			}.Build(),
			2: sgpb.Action_builder{
				Id: proto.Int64(2),
				Outputs: map[string]*sgpb.ResourceInteractions{
					fullPipeDigestStr: sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t2}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			3: sgpb.Action_builder{
				Id: proto.Int64(3),
				Inputs: map[string]*sgpb.ResourceInteractions{
					fullPipeDigestStr: sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t3}.Build(),
						},
					}.Build(),
				},
			}.Build(),
			4: sgpb.Action_builder{
				Id: proto.Int64(4),
				Inputs: map[string]*sgpb.ResourceInteractions{
					halfPipeDigestStr: sgpb.ResourceInteractions_builder{
						Interactions: []*sgpb.ResourceInteraction{
							sgpb.ResourceInteraction_builder{Timestamp: t3}.Build(),
						},
					}.Build(),
				},
			}.Build(),
		},
	}

	gotPipes, err := AllRiskyPipes(context.Background(), sg)
	if err != nil {
		t.Errorf("AllRiskyPipes returned unexpected error: %v", err)
	}
	wantPipes := map[int64]struct{}{1: {}}
	if diff := cmp.Diff(gotPipes, wantPipes); diff != "" {
		t.Errorf("AllRiskyPipes returned unexpected results, diff\n%s", diff)
	}
	gotPipePairs, err := AllPipePairs(context.Background(), sg, 1)
	if err != nil {
		t.Errorf("AllPipePairs returned unexpected error: %v", err)
	}
	wantPipePairs := map[int64]int64{3: 2}
	if diff := cmp.Diff(gotPipePairs, wantPipePairs); diff != "" {
		t.Errorf("AllPipePairs returned unexpected results, diff\n%s", diff)
	}
}
