// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
)

// treeNode represents a single action in the process tree with its children
// ordered by start time.
type treeNode struct {
	action   *sgpb.Action
	execPath string
	children []*treeNode
}

func getExecutablePath(a *sgpb.Action, resources map[pbdigest.Digest]*sgpb.Resource) string {
	digestStr := a.GetExecutableResourceDigest()
	if digestStr == "" {
		return ""
	}
	dg, err := pbdigest.NewFromString(digestStr)
	if err != nil {
		return ""
	}
	res, ok := resources[dg]
	if !ok || res.GetFileInfo() == nil {
		return ""
	}
	return res.GetFileInfo().GetPath()
}

// actionInfo holds the data collected per action for tree building.
type actionInfo struct {
	action      *sgpb.Action
	execPath    string
	childrenIDs []int64
}

// buildTree loads all actions and constructs the process tree.
func buildTree(ctx context.Context, sg sgtransform.SysGraph) ([]*treeNode, error) {
	resources, err := sg.Resources(ctx)
	if err != nil {
		return nil, err
	}

	actions, err := sgquery.MapAllActions(ctx, sg, func(a *sgpb.Action) (int64, *actionInfo, error) {
		return a.GetId(), &actionInfo{
			action:      a,
			execPath:    getExecutablePath(a, resources),
			childrenIDs: slices.Collect(maps.Keys(a.GetChildren())),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// Build treeNode map.
	nodes := make(map[int64]*treeNode, len(actions))
	for id, info := range actions {
		nodes[id] = &treeNode{
			action:   info.action,
			execPath: info.execPath,
		}
	}

	// Wire up children sorted by start_time.
	for id, info := range actions {
		children := make([]*treeNode, 0, len(info.childrenIDs))
		for _, cid := range info.childrenIDs {
			if child, ok := nodes[cid]; ok {
				children = append(children, child)
			}
		}
		sort.Slice(children, func(i, j int) bool {
			ti := children[i].action.GetStartTime().AsTime()
			tj := children[j].action.GetStartTime().AsTime()
			return ti.Before(tj)
		})
		nodes[id].children = children
	}

	// Find roots (actions with no parent or whose parent is not in the set).
	entryPoints := sg.Proto(ctx).GetEntryPointActionIds()
	if len(entryPoints) > 0 {
		roots := make([]*treeNode, 0, len(entryPoints))
		for _, eid := range entryPoints {
			if n, ok := nodes[eid]; ok {
				roots = append(roots, n)
			}
		}
		sort.Slice(roots, func(i, j int) bool {
			return roots[i].action.GetStartTime().AsTime().Before(roots[j].action.GetStartTime().AsTime())
		})
		return roots, nil
	}

	// Fallback: find nodes whose parent_action_id is 0 or not in the set.
	var roots []*treeNode
	for _, n := range nodes {
		pid := n.action.GetParentActionId()
		if pid == 0 {
			roots = append(roots, n)
		} else if _, ok := nodes[pid]; !ok {
			roots = append(roots, n)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].action.GetStartTime().AsTime().Before(roots[j].action.GetStartTime().AsTime())
	})
	return roots, nil
}

// renderOpts bundles all rendering configuration.
type renderOpts struct {
	MaxDepth     int
	Collapse     int
	ShowForks    bool
	ShowIDs      bool
	ShowCwd      bool
	ShowDuration bool
	AncestorID   int64 // if set, show only the ancestor path to this node
}

// renderTree writes the process tree to w.
func renderTree(w io.Writer, roots []*treeNode, opts renderOpts) {
	if opts.AncestorID != 0 {
		roots = pruneToAncestors(roots, opts.AncestorID)
	}
	for _, root := range roots {
		renderNode(w, root, 1, opts)
	}
}

// pruneToAncestors returns a copy of the tree containing only the path from
// the root to the node with the given action ID. Children of intermediate
// nodes are removed so only the direct ancestor chain is shown.
func pruneToAncestors(roots []*treeNode, targetID int64) []*treeNode {
	for _, root := range roots {
		if path := findAncestorPath(root, targetID); path != nil {
			return path
		}
	}
	return nil
}

// findAncestorPath returns a single-child chain from n down to the node
// matching targetID, or nil if targetID is not in this subtree.
func findAncestorPath(n *treeNode, targetID int64) []*treeNode {
	if n.action.GetId() == targetID {
		return []*treeNode{{action: n.action, execPath: n.execPath}}
	}
	for _, child := range n.children {
		if path := findAncestorPath(child, targetID); path != nil {
			pruned := &treeNode{action: n.action, execPath: n.execPath, children: path}
			return []*treeNode{pruned}
		}
	}
	return nil
}

func renderNode(w io.Writer, n *treeNode, depth int, opts renderOpts) {
	if opts.MaxDepth > 0 && depth > opts.MaxDepth {
		return
	}

	isFork := n.action.GetIsFork()

	// Fork collapsing: skip this node but recurse into children at the same depth.
	if isFork && !opts.ShowForks {
		for _, child := range n.children {
			renderNode(w, child, depth, opts)
		}
		return
	}

	// Format and print this node.
	prefix := strings.Repeat("+", depth) + " "
	argv := formatArgv(n)

	var annotations []string
	if opts.ShowIDs {
		annotations = append(annotations, fmt.Sprintf("id=%d", n.action.GetId()))
	}
	if isFork {
		annotations = append(annotations, "fork")
	}
	if docker, ok := n.action.GetMetadata()["docker"]; ok && docker != "" {
		annotations = append(annotations, fmt.Sprintf("container:%s", shortID(docker)))
	}
	if opts.ShowCwd && n.action.GetExecInfo() != nil {
		if cwd := n.action.GetExecInfo().GetWorkingDirectory(); cwd != "" {
			annotations = append(annotations, fmt.Sprintf("cwd=%s", cwd))
		}
	}
	if opts.ShowDuration {
		start := n.action.GetStartTime().AsTime()
		end := n.action.GetEndTime().AsTime()
		if !start.IsZero() && !end.IsZero() && end.After(start) {
			annotations = append(annotations, formatDuration(end.Sub(start)))
		}
	}

	line := prefix + argv
	if len(annotations) > 0 {
		line += "  [" + strings.Join(annotations, ", ") + "]"
	}
	fmt.Fprintln(w, line)

	// Render children with sibling collapsing.
	renderChildren(w, n.children, depth+1, opts)
}

// renderChildren renders a list of child nodes, applying sibling collapsing.
func renderChildren(w io.Writer, children []*treeNode, depth int, opts renderOpts) {
	if opts.Collapse <= 0 {
		for _, child := range children {
			renderNode(w, child, depth, opts)
		}
		return
	}

	i := 0
	for i < len(children) {
		// Find run of consecutive siblings with the same exec basename.
		// For fork-collapsed nodes, peek through to the exec'd child to
		// determine the effective basename.
		base := effectiveExecBase(children[i], opts.ShowForks)
		j := i + 1
		for j < len(children) && effectiveExecBase(children[j], opts.ShowForks) == base && base != "" {
			j++
		}

		runLen := j - i
		if runLen >= opts.Collapse {
			// Collapsed summary line.
			prefix := strings.Repeat("+", depth) + " "
			sample := formatArgv(effectiveNode(children[i], opts.ShowForks))
			fmt.Fprintf(w, "%s[%dx] %s\n", prefix, runLen, sample)
		} else {
			for k := i; k < j; k++ {
				renderNode(w, children[k], depth, opts)
			}
		}
		i = j
	}
}

// effectiveExecBase returns the exec basename for a node. If the node is a
// fork (and forks are hidden), it looks at the first exec'd descendant.
func effectiveExecBase(n *treeNode, showForks bool) string {
	return filepath.Base(effectiveNode(n, showForks).execPath)
}

// effectiveNode returns the node that would actually be rendered. For fork
// nodes (when forks are hidden), this walks down to the first non-fork child.
func effectiveNode(n *treeNode, showForks bool) *treeNode {
	if !showForks && n.action.GetIsFork() && len(n.children) > 0 {
		return effectiveNode(n.children[0], showForks)
	}
	return n
}

func formatArgv(n *treeNode) string {
	if n.action.GetExecInfo() != nil {
		argv := n.action.GetExecInfo().GetArgv()
		if len(argv) > 0 {
			return strings.Join(argv, " ")
		}
	}
	if n.execPath != "" {
		return n.execPath
	}
	return fmt.Sprintf("<action:%d>", n.action.GetId())
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Truncate(time.Millisecond).String()
}

// shortID returns the first 12 characters of an ID string (like Docker short IDs).
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
