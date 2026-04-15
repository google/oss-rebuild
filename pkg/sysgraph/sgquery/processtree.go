// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgquery

import (
	"context"

	"maps"
	"slices"

	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
)

// MinimalProcessTree is a minimal representation of a process tree in the sysgraph.
type MinimalProcessTree struct {
	ActionID int64
	Args     []string
	Children map[int64]*MinimalProcessTree
}

type actionDetails struct {
	node        *MinimalProcessTree
	isRoot      bool
	childrenIds []int64
}

// ProcessTree returns a tree of actions in the sysgraph.
// The key is the action id of the root of the tree.
// The value is the list of action ids of the children of the key.
func ProcessTree(ctx context.Context, sg ActionProvider) (*MinimalProcessTree, error) {
	root := &MinimalProcessTree{
		Children: make(map[int64]*MinimalProcessTree),
	}
	actions, err := MapAllActions(ctx, sg, func(a *sgpb.Action) (int64, actionDetails, error) {
		childrenIds := slices.Collect(maps.Keys(a.GetChildren()))
		return a.GetId(), actionDetails{
			node: &MinimalProcessTree{
				ActionID: a.GetId(),
				Args:     a.GetExecInfo().GetArgv(),
			},
			childrenIds: childrenIds,
			isRoot:      a.GetParentActionId() == 0,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	for _, a := range actions {
		a.node.Children = make(map[int64]*MinimalProcessTree, len(a.childrenIds))
		for _, childID := range a.childrenIds {
			a.node.Children[childID] = actions[childID].node
		}
		if a.isRoot {
			root.Children[a.node.ActionID] = a.node
		}
	}
	return root, nil
}
