// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgquery provides functions for querying the sysgraph.
package sgquery

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"maps"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"golang.org/x/sync/errgroup"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// ActionProvider specifies the methods needed to load actions from a sysgraph.
type ActionProvider interface {
	ActionIDs(context.Context) ([]int64, error)
	Action(ctx context.Context, id int64) (*sgpb.Action, error)
}

func rangeParallel[I any](ctx context.Context, inputs []I, f func(context.Context, I) error) error {
	eg, eCtx := errgroup.WithContext(ctx)
	eg.SetLimit(runtime.NumCPU())
	for _, input := range inputs {
		eg.Go(func() error {
			return f(eCtx, input)
		})
	}
	return eg.Wait()
}

func mapParallel[I any, O comparable](ctx context.Context, inputs []I, f func(context.Context, I) (O, error)) ([]O, error) {
	outputs := make([]O, 0, len(inputs))
	outputCh := make(chan O, len(inputs))
	var wg sync.WaitGroup
	wg.Add(1)
	var nilOutput O
	go func() {
		defer wg.Done()
		for {
			select {
			case output, ok := <-outputCh:
				if !ok {
					return
				}
				outputs = append(outputs, output)
			case <-ctx.Done():
				return
			}
		}
	}()
	err := rangeParallel(ctx, inputs, func(ctx context.Context, input I) error {
		output, err := f(ctx, input)
		if err != nil {
			return err
		}
		if output == nilOutput {
			return nil
		}
		select {
		case outputCh <- output:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		close(outputCh)
		return nil, err
	}
	close(outputCh)
	wg.Wait()
	return outputs, nil
}

type actionResource struct {
	aid int64
	rdg string
}

func actionEarliestOutput(ctx context.Context, sg ActionProvider, aids []int64) (map[int64]map[string]time.Time, error) {
	type outputMsg struct {
		outputs map[string]*sgpb.ResourceInteractions
		aid     int64
	}
	outputs, err := mapParallel(ctx, aids, func(ctx context.Context, aid int64) (*outputMsg, error) {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			return nil, err
		}
		return &outputMsg{a.GetOutputs(), aid}, nil
	})
	if err != nil {
		return nil, err
	}
	ret := make(map[int64]map[string]time.Time, len(aids))
	for _, output := range outputs {
		dgs := map[string]time.Time{}
		for dg, o := range output.outputs {
			ri := slices.MinFunc(o.GetInteractions(), func(a, b *sgpb.ResourceInteraction) int {
				return a.GetTimestamp().AsTime().Compare(b.GetTimestamp().AsTime())
			})
			t := ri.GetTimestamp().AsTime()
			if ctime, ok := dgs[dg]; !ok || ctime.After(t) {
				dgs[dg] = t
			}
		}
		ret[output.aid] = dgs
	}
	return ret, nil
}

func actionLatestInput(ctx context.Context, sg ActionProvider, aids []int64) (map[int64]map[string]time.Time, error) {
	type inputMgs struct {
		inputs map[string]*sgpb.ResourceInteractions
		aid    int64
	}
	outputs, err := mapParallel(ctx, aids, func(ctx context.Context, aid int64) (*inputMgs, error) {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			return &inputMgs{}, err
		}
		return &inputMgs{a.GetInputs(), aid}, nil
	})
	if err != nil {
		return nil, err
	}
	ret := make(map[int64]map[string]time.Time, len(aids))
	for _, input := range outputs {
		dgs := map[string]time.Time{}
		for dg, i := range input.inputs {
			ri := slices.MaxFunc(i.GetInteractions(), func(a, b *sgpb.ResourceInteraction) int {
				return a.GetTimestamp().AsTime().Compare(b.GetTimestamp().AsTime())
			})
			t := ri.GetTimestamp().AsTime()
			if ctime, ok := dgs[dg]; !ok || ctime.Before(t) {
				dgs[dg] = t
			}
		}
		ret[input.aid] = dgs
	}
	return ret, nil
}

// ResourceDependencies returns an adjacency list of resource dependencies for all actions in the
// sysgraph.
// Action A has a resource dependency on action B if A reads an output of B.
func ResourceDependencies(ctx context.Context, sg ActionProvider, source int64) (map[int64][]int64, error) {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	earliestOutputs, err := actionEarliestOutput(ctx, sg, aids)
	if err != nil {
		return nil, err
	}
	latestInput, err := actionLatestInput(ctx, sg, aids)
	if err != nil {
		return nil, err
	}
	resDepsMap := map[int64]map[int64]bool{}
	for _, inputAid := range aids {
		for _, outputAid := range aids {
			if isResourceDependency(inputAid, outputAid, earliestOutputs, latestInput) {
				if _, ok := resDepsMap[inputAid]; !ok {
					resDepsMap[inputAid] = map[int64]bool{}
				}
				resDepsMap[inputAid][outputAid] = true
			}
		}
	}
	resDeps := make(map[int64][]int64, len(resDepsMap))
	for aid, deps := range resDepsMap {
		resDeps[aid] = slices.Collect(maps.Keys(deps))
	}
	return resDeps, nil
}

func isResourceDependency(inputAid, outputAid int64, earliestOutputs map[int64]map[string]time.Time, latestInput map[int64]map[string]time.Time) bool {
	if inputAid == outputAid {
		return false
	}
	for dg, inputTime := range latestInput[inputAid] {
		outDgs, ok := earliestOutputs[outputAid]
		if !ok {
			continue
		}
		outTime, ok := outDgs[dg]
		if !ok {
			continue
		}
		if outTime.Before(inputTime) {
			return true
		}
	}
	return false
}

// FilterActions returns all actions in the sysgraph that match the given predicate.
// The predicate is applied in parallel. sg is generic to allow the filter to see its concrete type.
func FilterActions[T ActionProvider](ctx context.Context, sg T, f func(context.Context, T, *sgpb.Action) bool) ([]*sgpb.Action, error) {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	return mapParallel(ctx, aids, func(ctx context.Context, aid int64) (*sgpb.Action, error) {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			return nil, err
		}
		if f(ctx, sg, a) {
			return a, nil
		}
		return nil, nil
	})
}

// ErrNoActionFound is returned when no action matches a predicate.
var ErrNoActionFound = fmt.Errorf("no action found that matches the predicate")

// FindFirstBFS returns the first action in the sysgraph that matches the given predicate.
func FindFirstBFS(ctx context.Context, sg ActionProvider, roots []int64, f func(context.Context, *sgpb.Action) bool) (*sgpb.Action, error) {
	visited := map[int64]struct{}{}
	toVisit := roots
	for len(toVisit) > 0 {
		actions, err := MapActions(ctx, sg, toVisit, func(a *sgpb.Action) (int64, *sgpb.Action, error) {
			return a.GetId(), a, nil
		})
		if err != nil {
			return nil, err
		}
		toVisitMap := map[int64]struct{}{}
		for id, a := range actions {
			if _, ok := visited[id]; ok {
				continue
			}
			if f(ctx, a) {
				return a, nil
			}
			visited[id] = struct{}{}
			for child := range a.GetChildren() {
				if _, ok := visited[child]; !ok {
					toVisitMap[child] = struct{}{}
				}
			}
		}
		toVisit = slices.Collect(maps.Keys(toVisitMap))
	}
	return nil, ErrNoActionFound
}

// RangeActions ranges over all actions in the sysgraph in parallel.
func RangeActions(ctx context.Context, sg ActionProvider, f func(ctx context.Context, a *sgpb.Action) error) error {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return err
	}
	return rangeParallel(ctx, aids, func(ctx context.Context, aid int64) error {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			return err
		}
		return f(ctx, a)
	})
}

// MapAllActions maps over all actions in the sysgraph in parallel.
func MapAllActions[K comparable, V any](ctx context.Context, sg ActionProvider, f func(*sgpb.Action) (K, V, error)) (map[K]V, error) {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	return MapActions(ctx, sg, aids, f)
}

// MapActions maps over a subset of actions in the sysgraph in parallel.
func MapActions[K comparable, V any](ctx context.Context, sg ActionProvider, aids []int64, f func(*sgpb.Action) (K, V, error)) (map[K]V, error) {
	ret := make(map[K]V, len(aids))
	var smap sync.Map
	err := rangeParallel(ctx, aids, func(ctx context.Context, aid int64) error {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			return err
		}
		k, v, err := f(a)
		if err != nil {
			return err
		}
		smap.Store(k, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	smap.Range(func(k, v any) bool {
		ret[k.(K)] = v.(V)
		return true
	})
	return ret, nil
}

// ResourcesInteractions returns the resources that are inputs or outputs of the action or the
// executable of the action.
func ResourcesInteractions(action *sgpb.Action) map[string]*sgpb.ResourceInteractions {
	res := map[string]*sgpb.ResourceInteractions{}
	for dg, ris := range action.GetInputs() {
		if _, ok := res[dg]; !ok {
			res[dg] = &sgpb.ResourceInteractions{}
		}
		res[dg].SetInteractions(append(res[dg].GetInteractions(), ris.GetInteractions()...))
	}
	for dg, ris := range action.GetOutputs() {
		if _, ok := res[dg]; !ok {
			res[dg] = &sgpb.ResourceInteractions{}
		}
		res[dg].SetInteractions(append(res[dg].GetInteractions(), ris.GetInteractions()...))
	}
	if action.HasExecutableResourceDigest() {
		dg := action.GetExecutableResourceDigest()
		if _, ok := res[dg]; !ok {
			res[dg] = &sgpb.ResourceInteractions{}
		}
		res[dg].SetInteractions(append(res[dg].GetInteractions(), action.GetExecutable()))
	}
	return res
}

// TransitiveDeps contains the transitive dependencies of an action including the action itself.
type TransitiveDeps struct {
	Actions   map[int64]*sgpb.Action
	Resources map[pbdigest.Digest]*sgpb.Resource
}
type transitiveDepKeys struct {
	actions   map[int64]bool
	resources map[string]bool
}

// ActionResourceProvider is an ActionProvider that can also load resources.
type ActionResourceProvider interface {
	ActionProvider
	Resource(ctx context.Context, dg pbdigest.Digest) (*sgpb.Resource, error)
}

// bfs performs a breadth first search of the action parent-child tree starting at the given ids.
func bfs(ctx context.Context, sg ActionResourceProvider, ids []int64) (map[int64]*sgpb.Action, error) {
	visited := map[int64]*sgpb.Action{}
	toVisit := ids
	for len(toVisit) > 0 {
		actions, err := MapActions(ctx, sg, toVisit, func(a *sgpb.Action) (int64, *sgpb.Action, error) {
			return a.GetId(), a, nil
		})
		if err != nil {
			return nil, err
		}
		toVisitMap := map[int64]bool{}
		for id, a := range actions {
			if _, ok := visited[id]; ok {
				continue
			}
			visited[id] = a
			for child := range a.GetChildren() {
				if _, ok := visited[child]; !ok {
					toVisitMap[child] = true
				}
			}
		}
		toVisit = slices.Collect(maps.Keys(toVisitMap))
	}
	return visited, nil
}

// AllTransitiveDeps returns the transitive dependencies of the given actions including the actions
// themselves.
func AllTransitiveDeps(ctx context.Context, sg ActionResourceProvider, ids []int64) (map[int64]TransitiveDeps, error) {
	actions, err := bfs(ctx, sg, ids)
	if err != nil {
		return nil, err
	}
	visited := map[int64]bool{}
	topoOrder := make([]*sgpb.Action, 0, len(ids))
	for _, id := range ids {
		if visited[id] {
			continue
		}
		stack := make([]int64, 0, len(ids))
		stack = append(stack, id)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if visited[n] {
				continue
			}
			visited[n] = true
			a, ok := actions[n]
			if !ok {
				return nil, fmt.Errorf("action %d not found", id)
			}
			topoOrder = append(topoOrder, a)
			stack = append(stack, slices.Collect(maps.Keys(a.GetChildren()))...)
		}
	}
	transitiveDeps := make(map[int64]TransitiveDeps, len(ids))
	for idx := 0; idx < len(topoOrder); idx++ {
		a := topoOrder[len(topoOrder)-1-idx]
		actions := map[int64]*sgpb.Action{a.GetId(): a}
		resources := map[pbdigest.Digest]*sgpb.Resource{}
		for strDg := range ResourcesInteractions(a) {
			dg, err := pbdigest.NewFromString(strDg)
			if err != nil {
				return nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			resources[dg] = res
		}
		for dep := range a.GetChildren() {
			maps.Copy(actions, transitiveDeps[dep].Actions)
			maps.Copy(resources, transitiveDeps[dep].Resources)
		}
		transitiveDeps[a.GetId()] = TransitiveDeps{
			Actions:   actions,
			Resources: resources,
		}
	}
	return transitiveDeps, nil
}

// AllInputs returns all the inputs for the given action.
func AllInputs(ctx context.Context, sg ActionProvider, id int64) []*sgpb.ResourceInteraction {
	ins := map[int64][]*sgpb.ResourceInteraction{}
	allInputs(ctx, sg, id, ins)
	return ins[id]
}

// AllDependencies returns all the inputs for a given output.
func AllDependencies(ctx context.Context, sg ActionProvider, digest pbdigest.Digest) ([]*sgpb.ResourceInteraction, error) {
	dgStr := digest.String()
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range aids {
		a, err := sg.Action(ctx, a)
		if err != nil {
			return nil, err
		}
		if _, ok := a.GetOutputs()[dgStr]; ok {
			ins := map[int64][]*sgpb.ResourceInteraction{}
			err := allInputs(ctx, sg, a.GetId(), ins)
			if err != nil {
				return nil, err
			}
			return ins[a.GetId()], nil
		}
	}
	return nil, fmt.Errorf("output digest %s not found", digest)
}

func allInputs(ctx context.Context, sg ActionProvider, id int64, ins map[int64][]*sgpb.ResourceInteraction) error {
	if _, ok := ins[id]; ok {
		return nil
	}
	a, err := sg.Action(ctx, id)
	if err != nil {
		return err
	}
	allRis := []*sgpb.ResourceInteraction{}
	for _, ris := range a.GetInputs() {
		allRis = append(allRis, ris.GetInteractions()...)
	}
	for cid := range a.GetChildren() {
		err := allInputs(ctx, sg, cid, ins)
		if err != nil {
			return err
		}
		allRis = append(allRis, ins[cid]...)
	}
	ins[id] = allRis
	return nil
}

// AllRiskyPipes returns the set of action ids that are risky pipe actions.
// The risky pipe actions will have the metadata key "risky_pipe" set to "true".
func AllRiskyPipes(ctx context.Context, sg ActionProvider) (map[int64]struct{}, error) {
	res := make(map[int64]struct{})
	ids, err := sg.ActionIDs(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		action, err := sg.Action(ctx, id)
		if err != nil {
			return res, err
		}
		if action.GetMetadata()["risky_pipe"] == "true" {
			res[id] = struct{}{}
		}
	}
	return res, nil
}

// AllPipePairs returns a map that represents the child action pairs that are communicating between
// each other via the pipe/dup pattern for a given parent action.
// key is the action id of the read end dup action, and value is the action id of the write end of
// dup action. If two processes are communicating via a pipe between each other, one side must have
// a pipe resource as input and the other side must have the same pipe resource as output.
func AllPipePairs(ctx context.Context, sg ActionResourceProvider, parentActionID int64) (map[int64]int64, error) {
	readers := map[string]int64{}
	writers := map[string]int64{}
	res := make(map[int64]int64)
	children, err := sg.Action(ctx, parentActionID)
	if err != nil {
		return nil, err
	}

	for id := range children.GetChildren() {
		a, err := sg.Action(ctx, id)
		if err != nil {
			return nil, err
		}
		inputs := a.GetInputs()
		for dgstr := range inputs {
			dg, err := pbdigest.NewFromString(dgstr)
			if err != nil {
				return nil, err
			}

			r, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			if r.GetType() == sgpb.ResourceType_RESOURCE_TYPE_PIPE {
				readers[dgstr] = id
			}
		}
		outputs := a.GetOutputs()
		for dgstr := range outputs {
			dg, err := pbdigest.NewFromString(dgstr)
			if err != nil {
				return nil, err
			}
			r, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			if r.GetType() == sgpb.ResourceType_RESOURCE_TYPE_PIPE {
				writers[dgstr] = id
			}
		}
	}
	for pipe, r := range readers {
		if w, ok := writers[pipe]; ok {
			res[r] = w
		}
	}
	return res, nil
}

// ExitInfo represents the exit status of a process. See reference:
// https://github.com/cilium/tetragon/blob/73e67887dda99f0dc48cc0d05b70388ddd2f9936/api/v1/tetragon/tetragon.proto#L320
type ExitInfo struct {
	// Signal that the process received when it exited.
	Signal string
	// Status code on process exit.
	Status uint32
}

// AbnormalExits returns the cnt for all different abnormal exits (including action that exit with a
// signal or a non-zero exit status code). This function is used for Kokoro integ test.
func AbnormalExits(ctx context.Context, sg ActionProvider) (map[ExitInfo]int, error) {
	res := make(map[ExitInfo]int)
	ids, err := sg.ActionIDs(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		action, err := sg.Action(ctx, id)
		if err != nil {
			return res, err
		}
		if action.GetExitSignal() == "" && action.GetExitStatus() == 0 {
			continue
		}
		res[ExitInfo{
			Signal: action.GetExitSignal(),
			Status: action.GetExitStatus(),
		}]++
	}
	return res, nil
}
