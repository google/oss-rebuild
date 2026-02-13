// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgmatch provides utilities for matching sysgraphs.
package sgmatch

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// ExtractedValue contains either an action or resource.
type ExtractedValue struct {
	*sgpb.Action
	*sgpb.Resource
}

// HasAction returns true if the match contains an action.
func (m ExtractedValue) HasAction() bool {
	return m.Action != nil
}

// HasResource returns true if the match contains a resource.
func (m ExtractedValue) HasResource() bool {
	return m.Resource != nil
}

func (m ExtractedValue) String() string {
	if m.HasAction() {
		return m.Action.String()
	}
	return m.Resource.String()
}

// Action matches an action.
type Action interface {
	Matches(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error)
}

// ActionFunc matches an action and extracts no values.
type ActionFunc func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error)

// Matches returns true if the action matches the matcher.
func (m ActionFunc) Matches(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
	match, err := m(ctx, sg, a)
	if err != nil {
		return false, nil, err
	}
	if !match {
		return false, nil, nil
	}
	return true, nil, nil
}

// ActionWithValues matches an action and extracts values.
type ActionWithValues func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error)

// Matches returns true if the action matches the matcher and any extracted values.
func (m ActionWithValues) Matches(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
	return m(ctx, sg, a)
}

// AnyAction matches any action.
var AnyAction ActionFunc = anyAction

func anyAction(context.Context, sgtransform.SysGraph, *sgpb.Action) (bool, error) {
	return true, nil
}

// Traversal matches an edge in the sysgraph.
type Traversal interface {
	Traverse(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error)
}

// TraversalFunc matches an edge in the sysgraph.
type TraversalFunc func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error)

// Traverse matches an edge in the sysgraph.
func (m TraversalFunc) Traverse(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
	return m(ctx, sg, chain)
}

// AllActions matches all actions in the sysgraph.
func AllActions(action Action) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		var mu sync.Mutex
		var chains []Chain
		sgquery.FilterActions(ctx, sg, func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) bool {
			ok, evs, err := action.Matches(ctx, sg, a)
			if err != nil {
				return false
			}
			if ok {
				mu.Lock()
				chains = append(chains, Chain{Actions: []*sgpb.Action{a}, Values: evs})
				mu.Unlock()
			}
			return ok
		})
		return chains, nil
	}
}

// ActionExitSignal matches an action with a given exit signal.
func ActionExitSignal(signal string) ActionFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error) {
		return a.GetExitSignal() == signal, nil
	}
}

// ActionExitStatus matches an action with a given exit status.
func ActionExitStatus(status uint32) ActionFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error) {
		return a.GetExitStatus() == status, nil
	}
}

func allConsumers(ctx context.Context, sg sgtransform.SysGraph, dg string) ([]*sgpb.Action, error) {
	var consumers []*sgpb.Action
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, aid := range aids {
		a, err := sg.Action(ctx, aid)
		if err != nil {
			continue
		}
		if a.GetInputs() == nil {
			continue
		}
		if _, ok := a.GetInputs()[dg]; ok {
			consumers = append(consumers, a)
		}
	}
	return consumers, nil
}

// OutputToInputTraversal matches edges from an action that matches the given action matcher with an output
// that matches the given resource matcher to all actions that consume that resource
func OutputToInputTraversal(action Action, resource Resource) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		var chains []Chain
		for dgStr := range a.GetOutputs() {
			dg, err := pbdigest.NewFromString(dgStr)
			if err != nil {
				return nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			match, resEvs, err := resource.Matches(sg, res)
			if err != nil {
				return nil, err
			}
			if match {
				consumers, err := allConsumers(ctx, sg, dgStr)
				if err != nil {
					return nil, err
				}
				for _, consumer := range consumers {
					match, actionEvs, err := action.Matches(ctx, sg, consumer)
					if err != nil {
						return nil, err
					}
					if match {
						evs := maps.Clone(resEvs)
						for k, v := range actionEvs {
							evs[k] = append(evs[k], v...)
						}
						chains = append(chains, chain.add(consumer, evs))
					}
				}
			}
		}
		return chains, nil
	}
}

func allProducers(ctx context.Context, sg sgtransform.SysGraph, dg string) (iter.Seq[*sgpb.Action], error) {
	aids, err := sg.ActionIDs(ctx)
	if err != nil {
		return nil, err
	}
	return func(yield func(*sgpb.Action) bool) {
		for _, aid := range aids {
			a, err := sg.Action(ctx, aid)
			if err != nil {
				continue
			}
			if a.GetOutputs() == nil {
				continue
			}
			if _, ok := a.GetOutputs()[dg]; ok {
				if !yield(a) {
					return
				}
			}
		}
	}, nil
}

// InputToProducerTraversal finds a connection between a consumer action and a producer action
// of a specific resource.
//
// It starts with an action (the consumer) and looks at its inputs. If an input
// matches the given 'resource' matcher, it then looks for actions that *produced*
// that input. If a producing action matches the given 'action' matcher, a match is found.
//
// Example:
//
//	Action A (Consumer): /usr/bin/ar x libfoo.a  (Inputs: libfoo.a)
//	Resource: libfoo.a
//	Action B (Producer): /usr/bin/ar r libfoo.a foo.o bar.o (Outputs: libfoo.a)
//
// InputToProducerTraversal(ActionExecutable("/usr/bin/ar"), ResourcePathSuffix(".a"))
// would match the chain [Action A, Action B].
func InputToProducerTraversal(action Action, resource Resource) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		var chains []Chain
		for dgStr := range a.GetInputs() {
			dg, err := pbdigest.NewFromString(dgStr)
			if err != nil {
				return nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			match, resEvs, err := resource.Matches(sg, res)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			producers, err := allProducers(ctx, sg, dgStr)
			if err != nil {
				return nil, err
			}
			for producer := range producers {
				match, actionEvs, err := action.Matches(ctx, sg, producer)
				if err != nil {
					return nil, err
				}
				if !match {
					continue
				}
				evs := maps.Clone(resEvs)
				for k, v := range actionEvs {
					evs[k] = append(evs[k], v...)
				}
				chains = append(chains, chain.add(producer, evs))
			}
		}
		return chains, nil
	}
}

// UnproducedResource matches resources that are consumed but not produced by any action.
func UnproducedResource(resource Resource) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		var chains []Chain
		hasAny := func(seq iter.Seq[*sgpb.Action]) bool {
			for range seq {
				return true
			}
			return false
		}
		for dgStr := range a.GetInputs() {
			dg, err := pbdigest.NewFromString(dgStr)
			if err != nil {
				return nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return nil, err
			}
			match, resEvs, err := resource.Matches(sg, res)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			producers, err := allProducers(ctx, sg, dgStr)
			if err != nil {
				return nil, err
			}
			if hasAny(producers) {
				continue
			}
			if chain.Values == nil && len(resEvs) > 0 {
				chain.Values = map[string][]ExtractedValue{}
			}
			// Update in place since we're not traversing to a new action.
			for k, v := range resEvs {
				chain.Values[k] = append(chain.Values[k], v...)
			}
			chains = append(chains, chain)
		}
		return chains, nil
	}
}

// ParentToChildrenTraversal matches edges from an action that matches the given action matcher to all
// actions that are children of that action.
func ParentToChildrenTraversal(action Action) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		children := a.GetChildren()
		var chains []Chain
		for aid := range children {
			child, err := sg.Action(ctx, aid)
			if err != nil {
				return nil, err
			}
			match, evs, err := action.Matches(ctx, sg, child)
			if err != nil {
				return nil, err
			}
			if match {
				chains = append(chains, chain.add(child, evs))
			}

		}
		return chains, nil
	}
}

// ChildToParentTraversal matches edges from an action that matches the given action matcher to the parent
// action of that action.
func ChildToParentTraversal(action Action) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		parent, err := sg.Action(ctx, a.GetParentActionId())
		if err != nil {
			return nil, err
		}
		match, evs, err := action.Matches(ctx, sg, parent)
		if err != nil {
			return nil, err
		}
		if !match {
			return nil, nil
		}
		return []Chain{chain.add(parent, evs)}, nil
	}
}

func ancestors(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) iter.Seq2[*sgpb.Action, error] {
	return func(yield func(*sgpb.Action, error) bool) {
		current := a
		var err error
		for {
			if !current.HasParentActionId() {
				return
			}
			current, err = sg.Action(ctx, current.GetParentActionId())
			if !yield(current, err) {
				return
			}
		}
	}
}

// ActionToAllAncestorsTraversal matches edges from an action that matches the given action matcher to all
// ancestors of that action.
func ActionToAllAncestorsTraversal(action Action) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		for current, err := range ancestors(ctx, sg, a) {
			if err != nil {
				return nil, err
			}
			match, evs, err := action.Matches(ctx, sg, current)
			if err != nil {
				return nil, err
			}
			if !match {
				return nil, nil
			}
			chain = chain.add(current, evs)
		}
		return []Chain{chain}, nil
	}
}

// ActionToAnyAncestorTraversal matches edges from an action that matches the given action matcher to any
// ancestor of that action.
func ActionToAnyAncestorTraversal(action Action) TraversalFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, chain Chain) ([]Chain, error) {
		a, ok := chain.Latest()
		if !ok {
			return nil, nil
		}
		var chains []Chain
		for current, err := range ancestors(ctx, sg, a) {
			if err != nil {
				return nil, err
			}
			match, evs, err := action.Matches(ctx, sg, current)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			chains = append(chains, chain.add(current, evs))
		}
		return chains, nil
	}
}

// ExtractAction extracts an action from a matcher that returns a map of matches.
func ExtractAction(key string, action Action) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		match, evs, err := action.Matches(ctx, sg, a)
		if err != nil {
			return false, nil, err
		}
		if !match {
			return false, nil, nil
		}
		if evs == nil {
			evs = map[string][]ExtractedValue{}
		}
		evs[key] = append(evs[key], ExtractedValue{Action: a})
		return true, evs, nil
	}
}

// ExtractResource extracts a resource from a matcher that returns a map of matches.
func ExtractResource(key string, resource Resource) ResourceWithValues {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error) {
		match, evs, err := resource.Matches(sg, res)
		if err != nil {
			return false, nil, err
		}
		if !match {
			return false, nil, nil
		}
		if evs == nil {
			evs = map[string][]ExtractedValue{}
		}
		evs[key] = append(evs[key], ExtractedValue{Resource: res})
		return true, evs, nil
	}
}

// ActionExecutable matches an action with a given executable resource.
func ActionExecutable(resource Resource) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		dg, err := pbdigest.NewFromString(a.GetExecutableResourceDigest())
		if err != nil {
			return false, nil, err
		}
		res, err := sg.Resource(ctx, dg)
		if err != nil {
			return false, nil, err
		}
		return resource.Matches(sg, res)
	}
}

// ActionArgv matches an action with a given argv.
func ActionArgv(argv []string) ActionFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error) {
		return slices.Equal(a.GetExecInfo().GetArgv(), argv), nil
	}
}

// ActionArgvContains matches an action which contains a given arg in its argv.
func ActionArgvContains(arg string) ActionFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error) {
		return slices.Contains(a.GetExecInfo().GetArgv(), arg), nil
	}
}

// ActionWithNone matches an action with none of the given matchers.
func ActionWithNone(ActionChainMatchers ...Action) ActionFunc {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, error) {
		for _, matcher := range ActionChainMatchers {
			match, _, err := matcher.Matches(ctx, sg, a)
			if err != nil {
				return false, err
			}
			if match {
				return false, nil
			}
		}
		return true, nil
	}
}

// ActionWithAll matches an action with all of the given matchers.
func ActionWithAll(ActionChainMatchers ...Action) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		var allMatches map[string][]ExtractedValue
		for _, matcher := range ActionChainMatchers {
			match, evs, err := matcher.Matches(ctx, sg, a)
			if err != nil {
				return false, nil, err
			}
			if !match {
				return false, nil, nil
			}
			if allMatches == nil && len(evs) > 0 {
				allMatches = map[string][]ExtractedValue{}
			}
			for k, v := range evs {
				allMatches[k] = append(allMatches[k], v...)
			}
		}
		return true, allMatches, nil
	}
}

// Chain is a chain of actions.
type Chain struct {
	Actions []*sgpb.Action
	Values  map[string][]ExtractedValue
}

func (c Chain) add(a *sgpb.Action, evs map[string][]ExtractedValue) Chain {
	newVals := make(map[string][]ExtractedValue, len(c.Values))
	for k, v := range c.Values {
		newVals[k] = slices.Clone(v)
	}
	for k, v := range evs {
		newVals[k] = append(newVals[k], v...)
	}
	return Chain{
		Actions: append(slices.Clone(c.Actions), a),
		Values:  newVals,
	}
}

// Latest returns the most recently matched action in the chain.
func (c Chain) Latest() (*sgpb.Action, bool) {
	if len(c.Actions) == 0 {
		return nil, false
	}
	return c.Actions[len(c.Actions)-1], true
}

// Edges describe a path through the sysgraph to match.
type Edges []Traversal

// Chains returns the chains of actions that match the list of edges starting from any action in the sysgraph.
func (m Edges) AllChains(ctx context.Context, sg sgtransform.SysGraph) ([]Chain, error) {
	// Initialize with a single empty chain so that the first traversal
	// will trigger.
	chains := []Chain{{}}
	for _, edge := range m {
		var newChains []Chain
		for _, chain := range chains {
			newChain, err := edge.Traverse(ctx, sg, chain)
			if err != nil {
				return nil, err
			}
			newChains = append(newChains, newChain...)
		}
		chains = newChains
	}
	return chains, nil
}

// Chains returns the chains of actions that match the list of edges starting from any action in the sysgraph.
func (m Edges) AllUniqueChains(ctx context.Context, sg sgtransform.SysGraph) ([]Chain, error) {
	chains, err := m.AllChains(ctx, sg)
	if err != nil {
		return nil, err
	}
	uniqueChains := make(map[string]Chain, len(chains))
	for _, chain := range chains {
		var sb strings.Builder
		for _, a := range chain.Actions {
			sb.WriteString(fmt.Sprintf("a:%d,", a.GetId()))
		}
		keys := make([]string, 0, len(chain.Values))
		for k := range chain.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := chain.Values[k]
			sb.WriteString(fmt.Sprintf(" %s=", k))
			for _, v := range v {
				if v.Action != nil {
					sb.WriteString(fmt.Sprintf("a:%d,", v.Action.GetId()))
				}
				if v.Resource != nil {
					sb.WriteString(fmt.Sprintf("r:%s,", v.Resource.GetFileInfo().GetPath()))
				}
			}
		}
		uniqueChains[sb.String()] = chain
	}
	return slices.Collect(maps.Values(uniqueChains)), nil
}

// ActionOutput matches an action with an output that matches the given resource matcher.
func ActionOutput(rmatcher Resource) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		var allValues map[string][]ExtractedValue
		anyMatched := false
		for dgStr := range a.GetOutputs() {
			dg, err := pbdigest.NewFromString(dgStr)
			if err != nil {
				return false, nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return false, nil, err
			}
			match, evs, err := rmatcher.Matches(sg, res)
			if err != nil {
				return false, nil, err
			}
			if match {
				if allValues == nil && len(evs) > 0 {
					allValues = map[string][]ExtractedValue{}
				}
				for k, v := range evs {
					allValues[k] = append(allValues[k], v...)
				}
				anyMatched = true
			}
		}
		if anyMatched {
			return true, allValues, nil
		}
		return false, nil, nil
	}
}

// ActionWithAny matches an action with any of the given matchers.
func ActionWithAny(matchers ...Action) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		for _, matcher := range matchers {
			match, evs, err := matcher.Matches(ctx, sg, a)
			if err != nil {
				return false, nil, err
			}
			if match {
				return true, evs, nil
			}
		}
		return false, nil, nil
	}
}

// ActionInput matches an action with an input that matches the given resource matcher.
func ActionInput(rmatcher Resource) ActionWithValues {
	return func(ctx context.Context, sg sgtransform.SysGraph, a *sgpb.Action) (bool, map[string][]ExtractedValue, error) {
		var allValues map[string][]ExtractedValue
		anyMatched := false
		for dgStr := range a.GetInputs() {
			dg, err := pbdigest.NewFromString(dgStr)
			if err != nil {
				return false, nil, err
			}
			res, err := sg.Resource(ctx, dg)
			if err != nil {
				return false, nil, err
			}
			match, evs, err := rmatcher.Matches(sg, res)
			if err != nil {
				return false, nil, err
			}
			if match {
				if allValues == nil && len(evs) > 0 {
					allValues = map[string][]ExtractedValue{}
				}
				for k, v := range evs {
					allValues[k] = append(allValues[k], v...)
				}
				anyMatched = true
			}
		}
		if anyMatched {
			return true, allValues, nil
		}
		return false, nil, nil
	}
}

// Resource matches a resource.
type Resource interface {
	Matches(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error)
}

// ResourceFunc matches a resource and extracts no values.
type ResourceFunc func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error)

// Matches returns true if the resource matches the matcher.
func (m ResourceFunc) Matches(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error) {
	match, err := m(sg, res)
	return match, nil, err
}

// ResourceWithValues matches a resource and extracts values.
type ResourceWithValues func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error)

// Matches returns true if the resource matches the matcher and any extracted values.
func (m ResourceWithValues) Matches(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error) {
	return m(sg, res)
}

// AnyResource matches any resource.
var AnyResource ResourceFunc = anyResource

func anyResource(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
	return true, nil
}

// ResourcePath matches a resource with a given path.
func ResourcePath(path string) ResourceFunc {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
		return res.GetFileInfo().GetPath() == path, nil
	}
}

// ResourcePathSuffix matches a resource with a path that ends with a given suffix.
func ResourcePathSuffix(suffix string) ResourceFunc {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
		return strings.HasSuffix(res.GetFileInfo().GetPath(), suffix), nil
	}
}

// ResourcePathPrefix matches a resource with a path that starts with a given prefix.
func ResourcePathPrefix(prefix string) ResourceFunc {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
		return strings.HasPrefix(res.GetFileInfo().GetPath(), prefix), nil
	}
}

// ResourcePathRegexp matches a resource with a path that matches a given regexp.
func ResourcePathRegexp(regexp *regexp.Regexp) ResourceFunc {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
		return regexp.MatchString(res.GetFileInfo().GetPath()), nil
	}
}

// ResourceWithAll matches a resource with all of the given matchers.
func ResourceWithAll(matchers ...Resource) ResourceWithValues {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, map[string][]ExtractedValue, error) {
		var allMatches map[string][]ExtractedValue
		for _, matcher := range matchers {
			match, evs, err := matcher.Matches(sg, res)
			if err != nil {
				return false, nil, err
			}
			if !match {
				return false, nil, nil
			}
			if allMatches == nil && len(evs) > 0 {
				allMatches = map[string][]ExtractedValue{}
			}
			for k, v := range evs {
				allMatches[k] = append(allMatches[k], v...)
			}
		}
		return true, allMatches, nil
	}
}

// ResourceWithNone matches a resource with none of the given matchers.
func ResourceWithNone(matchers ...Resource) ResourceFunc {
	return func(sg sgtransform.SysGraph, res *sgpb.Resource) (bool, error) {
		for _, matcher := range matchers {
			match, _, err := matcher.Matches(sg, res)
			if err != nil {
				return false, err
			}
			if match {
				return false, nil
			}
		}
		return true, nil
	}
}
