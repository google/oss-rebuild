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

// containerEntry represents a container-internal subtree root found via
// docker metadata transitions.
type containerEntry struct {
	node        *treeNode
	containerID string
}

// graftContainers finds docker CLI commands (docker run/exec) and matches
// them to container-internal subtrees. Matched container subtrees are grafted
// as children of the docker command, and the containerd/shim/runc
// infrastructure that previously hosted them is pruned.
//
// The matching works as follows:
//   - Walk the tree to find container entry points: nodes where the "docker"
//     metadata transitions from empty (or a different value) to a container ID.
//   - Walk the tree to find docker CLI commands (docker run, exec, build).
//   - For docker exec: match by comparing the command tail in argv with the
//     container entry point's argv.
//   - Fallback: match by timing (container starts after docker command).
//   - Grafted entry points are removed from their original parent, and
//     infrastructure nodes left without meaningful children are pruned.
func graftContainers(roots []*treeNode) []*treeNode {
	// Phase 1: collect container entry points.
	var entries []containerEntry
	walkTree(roots, "", func(n *treeNode, parentDockerID string) string {
		dockerID := n.action.GetMetadata()["docker"]
		if dockerID != "" && dockerID != parentDockerID {
			entries = append(entries, containerEntry{node: n, containerID: dockerID})
		}
		if dockerID != "" {
			return dockerID
		}
		return parentDockerID
	})
	if len(entries) == 0 {
		return roots
	}

	// Phase 2: collect docker CLI commands.
	var dockerCmds []*treeNode
	walkTreeNodes(roots, func(n *treeNode) {
		if isDockerCLI(n) {
			dockerCmds = append(dockerCmds, n)
		}
	})
	if len(dockerCmds) == 0 {
		return roots
	}

	// Phase 3: match docker commands to container entries.
	grafted := map[*treeNode]bool{}

	// Sort docker commands by start time so timing-based matching is
	// deterministic.
	sort.Slice(dockerCmds, func(i, j int) bool {
		return dockerCmds[i].action.GetStartTime().AsTime().Before(dockerCmds[j].action.GetStartTime().AsTime())
	})

	for _, cmd := range dockerCmds {
		argv := cmd.action.GetExecInfo().GetArgv()
		subCmd, cmdArgs := parseDockerSubcommand(argv)
		if subCmd == "" {
			continue
		}

		var matched *containerEntry
		switch subCmd {
		case "exec":
			matched = matchDockerExec(cmdArgs, entries, grafted)
		case "run":
			matched = matchDockerRun(cmd, entries, grafted)
		case "build":
			matched = matchByTiming(cmd, entries, grafted)
		}
		if matched == nil {
			matched = matchByTiming(cmd, entries, grafted)
		}
		if matched != nil {
			cmd.children = append(cmd.children, matched.node)
			grafted[matched.node] = true
		}
	}

	if len(grafted) == 0 {
		return roots
	}

	// Phase 4: remove grafted nodes from their original parents and prune
	// empty infrastructure.
	graftTargets := map[*treeNode]bool{}
	for _, cmd := range dockerCmds {
		graftTargets[cmd] = true
	}
	roots = removeGrafted(roots, grafted, graftTargets)
	roots = pruneInfrastructure(roots)
	return roots
}

// walkTree traverses the tree, calling f on each node with the parent's docker
// metadata value. f returns the docker ID to propagate to children.
func walkTree(nodes []*treeNode, parentDockerID string, f func(n *treeNode, parentDockerID string) string) {
	for _, n := range nodes {
		dockerID := f(n, parentDockerID)
		walkTree(n.children, dockerID, f)
	}
}

// walkTreeNodes traverses the tree, calling f on each node.
func walkTreeNodes(nodes []*treeNode, f func(n *treeNode)) {
	for _, n := range nodes {
		f(n)
		walkTreeNodes(n.children, f)
	}
}

// isDockerCLI returns true if the node represents a docker CLI invocation.
func isDockerCLI(n *treeNode) bool {
	base := filepath.Base(n.execPath)
	if base != "docker" {
		return false
	}
	if n.action.GetIsFork() {
		return false
	}
	argv := n.action.GetExecInfo().GetArgv()
	if len(argv) < 2 {
		return false
	}
	sub, _ := parseDockerSubcommand(argv)
	return sub == "exec" || sub == "run" || sub == "build"
}

// parseDockerSubcommand extracts the docker subcommand and remaining args.
// Skips global flags.
func parseDockerSubcommand(argv []string) (string, []string) {
	i := 1
	for i < len(argv) {
		arg := argv[i]
		// Skip global docker flags that take a value.
		if arg == "-H" || arg == "--host" || arg == "--config" || arg == "--context" || arg == "-l" || arg == "--log-level" {
			i += 2
			continue
		}
		if strings.HasPrefix(arg, "-") {
			i++
			continue
		}
		return arg, argv[i+1:]
	}
	return "", nil
}

// matchDockerExec tries to match a "docker exec" command to a container entry
// by comparing the command suffix with the entry's argv.
func matchDockerExec(args []string, entries []containerEntry, grafted map[*treeNode]bool) *containerEntry {
	// docker exec [flags] <container> <cmd> [args...]
	// Find the container name and command.
	var cmdStart int
	foundContainer := false
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			// Skip flags; some take values.
			if args[i] == "-e" || args[i] == "--env" || args[i] == "-w" || args[i] == "--workdir" || args[i] == "-u" || args[i] == "--user" {
				i++
			}
			continue
		}
		if !foundContainer {
			foundContainer = true
			continue // skip the container name
		}
		cmdStart = i
		break
	}
	if cmdStart == 0 || cmdStart >= len(args) {
		return nil
	}
	execCmd := args[cmdStart:]

	// Find an entry whose argv matches the exec command.
	for i := range entries {
		e := &entries[i]
		if grafted[e.node] {
			continue
		}
		entryArgv := e.node.action.GetExecInfo().GetArgv()
		if argvSuffixMatch(execCmd, entryArgv) {
			return e
		}
	}
	return nil
}

// argvSuffixMatch returns true if the exec command matches the entry's argv.
// The entry argv basenames should match the command (the entry may have full
// paths while the exec command has basenames).
func argvSuffixMatch(execCmd, entryArgv []string) bool {
	if len(execCmd) == 0 || len(entryArgv) == 0 {
		return false
	}
	// Compare the first element by basename, rest literally.
	if filepath.Base(execCmd[0]) != filepath.Base(entryArgv[0]) {
		return false
	}
	// For single-command matches, basename match is sufficient.
	if len(execCmd) == 1 {
		return true
	}
	// Compare remaining args. The entry might have more or fewer args
	// (e.g. shell expansion), so check prefix.
	limit := len(execCmd)
	if limit > len(entryArgv) {
		limit = len(entryArgv)
	}
	for i := 1; i < limit; i++ {
		if execCmd[i] != entryArgv[i] {
			return false
		}
	}
	return true
}

// matchDockerRun tries to match a "docker run" command to a container entry
// by timing (container starts after docker run).
func matchDockerRun(cmd *treeNode, entries []containerEntry, grafted map[*treeNode]bool) *containerEntry {
	cmdStart := cmd.action.GetStartTime().AsTime()
	if cmdStart.IsZero() {
		return nil
	}
	var best *containerEntry
	var bestStart time.Time
	for i := range entries {
		e := &entries[i]
		if grafted[e.node] {
			continue
		}
		entryStart := e.node.action.GetStartTime().AsTime()
		if entryStart.IsZero() || entryStart.Before(cmdStart) {
			continue
		}
		if best == nil || entryStart.Before(bestStart) {
			best = e
			bestStart = entryStart
		}
	}
	return best
}

// matchByTiming matches a docker command to the temporally closest ungrafted
// container entry that starts after the command.
func matchByTiming(cmd *treeNode, entries []containerEntry, grafted map[*treeNode]bool) *containerEntry {
	return matchDockerRun(cmd, entries, grafted) // same logic
}

// removeGrafted removes grafted nodes from their original parent's children
// lists. It does not recurse into graft targets (docker CLI nodes) since
// their newly-appended children are the grafted entries themselves.
func removeGrafted(roots []*treeNode, grafted map[*treeNode]bool, graftTargets map[*treeNode]bool) []*treeNode {
	var filtered []*treeNode
	for _, r := range roots {
		if grafted[r] {
			continue
		}
		if !graftTargets[r] {
			r.children = removeGraftedChildren(r.children, grafted, graftTargets)
		}
		filtered = append(filtered, r)
	}
	return filtered
}

func removeGraftedChildren(children []*treeNode, grafted map[*treeNode]bool, graftTargets map[*treeNode]bool) []*treeNode {
	var result []*treeNode
	for _, c := range children {
		if grafted[c] {
			continue
		}
		if !graftTargets[c] {
			c.children = removeGraftedChildren(c.children, grafted, graftTargets)
		}
		result = append(result, c)
	}
	return result
}

// pruneInfrastructure removes containerd-shim, runc, and docker-init nodes
// that have no remaining children (after grafting removed their meaningful
// subtrees).
func pruneInfrastructure(roots []*treeNode) []*treeNode {
	var result []*treeNode
	for _, r := range roots {
		r.children = pruneInfrastructure(r.children)
		if isInfrastructure(r) && len(r.children) == 0 {
			continue
		}
		result = append(result, r)
	}
	return result
}

// isInfrastructure returns true for docker/container infrastructure processes
// that should be pruned when they have no meaningful children.
var infrastructureBasenames = map[string]bool{
	"containerd-shim-runc-v2": true,
	"containerd-shim":         true,
	"runc":                    true,
	"docker-runc":             true,
	"docker-init":             true,
	"buildkit-runc":           true,
}

func isInfrastructure(n *treeNode) bool {
	base := filepath.Base(n.execPath)
	return infrastructureBasenames[base]
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

// renderTree writes the process tree to w. It first grafts container-internal
// subtrees onto their triggering docker commands.
func renderTree(w io.Writer, roots []*treeNode, opts renderOpts) {
	roots = graftContainers(roots)
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
