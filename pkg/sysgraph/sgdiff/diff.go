// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgdiff provides functionality for comparing two sysgraphs.
package sgdiff

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
)

// Options controls the diff behavior.
type Options struct {
	// MaxItemsPerCategory limits items shown per section (0 = unlimited).
	MaxItemsPerCategory int
	// Verbose shows all items without truncation.
	Verbose bool
	// NormalizationRules filters expected differences.
	NormalizationRules []NormalizationRule
}

// DefaultOptions returns sensible defaults for diff options.
func DefaultOptions() Options {
	return Options{
		MaxItemsPerCategory: 10,
		NormalizationRules:  DefaultNormalizationRules(),
	}
}

// SysGraphDiff represents the differences between two sysgraphs.
type SysGraphDiff struct {
	// OldID and NewID identify the compared sysgraphs.
	OldID string
	NewID string

	// SecurityAlerts for concerning changes.
	SecurityAlerts []SecurityAlert

	// Categorized changes.
	Executables ExecutableDiff
	Network     NetworkDiff
	Files       FileDiff
	Structure   StructureDiff

	// NormalizedCounts tracks filtered items by category.
	NormalizedCounts map[string]int
}

// SecurityAlert represents a security-relevant change.
type SecurityAlert struct {
	Severity    string            // "critical", "warning", "info"
	Category    string            // "executable", "network", "env", "script"
	Description string            // Human-readable description
	ActionID    int64             // Related action ID
	Details     map[string]string // Additional context
}

// ExecutableDiff tracks executable changes.
type ExecutableDiff struct {
	Added   []ExecutableChange
	Removed []ExecutableChange
	Changed []ExecutableChange
	// Matched pairs executables by basename that exist in both graphs.
	Matched []ExecutableMatch
}

// ExecutableChange describes a change to an executable.
type ExecutableChange struct {
	Path     string
	Digest   pbdigest.Digest
	ActionID int64
	Argv     []string
	Context  string // Parent process chain
}

// ExecutableMatch pairs old and new versions of the same executable (by basename).
type ExecutableMatch struct {
	Basename string
	Old      ExecutableChange
	New      ExecutableChange
}

// NetworkDiff tracks network connection changes.
type NetworkDiff struct {
	Added   []NetworkChange
	Removed []NetworkChange
}

// NetworkChange describes a network connection change.
type NetworkChange struct {
	Protocol string
	Address  string
	ActionID int64
	Context  string
}

// FileDiff tracks file changes.
type FileDiff struct {
	Added      []FileChange
	Removed    []FileChange
	Changed    []FileChange
	Normalized []FileChange // Changes filtered by normalization rules
}

// FileChange describes a file change.
type FileChange struct {
	Path            string
	OldDigest       pbdigest.Digest
	NewDigest       pbdigest.Digest
	SizeDelta       int64
	NormalizeReason string // If non-empty, why this was normalized
}

// StructureDiff tracks process tree changes.
type StructureDiff struct {
	NewSpawns     []ProcessSpawn
	RemovedSpawns []ProcessSpawn
	// NewEdges are parent->child basename pairs only in the new graph.
	NewEdges []TreeEdge
	// RemovedEdges are parent->child basename pairs only in the old graph.
	RemovedEdges []TreeEdge
	// CommonEdges are edges present in both (for reference).
	CommonEdgeCount int
}

// TreeEdge represents a normalized parent->child relationship by basename.
type TreeEdge struct {
	ParentBasename string
	ChildBasename  string
	// Example instances from the graph (for context).
	Examples []ProcessSpawn
}

// ProcessSpawn describes a process spawning relationship.
type ProcessSpawn struct {
	ParentActionID int64
	ChildActionID  int64
	ParentExe      string
	ChildExe       string
	Argv           []string
}

// Diff compares two sysgraphs and returns their differences.
func Diff(ctx context.Context, old, new sgtransform.SysGraph, opts Options) (*SysGraphDiff, error) {
	diff := &SysGraphDiff{
		OldID:            old.Proto(ctx).GetId(),
		NewID:            new.Proto(ctx).GetId(),
		NormalizedCounts: make(map[string]int),
	}

	// Collect resources from both graphs.
	oldResources, err := old.Resources(ctx)
	if err != nil {
		return nil, err
	}
	newResources, err := new.Resources(ctx)
	if err != nil {
		return nil, err
	}

	// Compare executables and network connections.
	if err := diffExecutablesAndNetwork(ctx, old, new, oldResources, newResources, diff, opts); err != nil {
		return nil, err
	}

	// Compare files.
	diffFiles(oldResources, newResources, diff, opts)

	// Compare process tree structure.
	if err := diffStructure(ctx, old, new, diff); err != nil {
		return nil, err
	}

	// Generate security alerts.
	generateSecurityAlerts(diff)

	return diff, nil
}

// diffExecutablesAndNetwork extracts and compares executables and network connections.
func diffExecutablesAndNetwork(
	ctx context.Context,
	old, new sgtransform.SysGraph,
	oldResources, newResources map[pbdigest.Digest]*sgpb.Resource,
	diff *SysGraphDiff,
	_ Options,
) error {
	oldExecs := make(map[string]ExecutableChange) // path -> change
	newExecs := make(map[string]ExecutableChange)
	oldNetwork := make(map[string]NetworkChange) // protocol://address -> change
	newNetwork := make(map[string]NetworkChange)

	// Process old graph.
	oldActionIDs, err := old.ActionIDs(ctx)
	if err != nil {
		return err
	}
	for _, aid := range oldActionIDs {
		action, err := old.Action(ctx, aid)
		if err != nil {
			return err
		}
		collectExecutablesAndNetwork(action, oldResources, oldExecs, oldNetwork)
	}

	// Process new graph.
	newActionIDs, err := new.ActionIDs(ctx)
	if err != nil {
		return err
	}
	for _, aid := range newActionIDs {
		action, err := new.Action(ctx, aid)
		if err != nil {
			return err
		}
		collectExecutablesAndNetwork(action, newResources, newExecs, newNetwork)
	}

	// Compare executables by basename to find matches.
	// Group by basename first.
	oldByBasename := make(map[string][]ExecutableChange)
	newByBasename := make(map[string][]ExecutableChange)
	for _, exec := range oldExecs {
		base := basename(exec.Path)
		oldByBasename[base] = append(oldByBasename[base], exec)
	}
	for _, exec := range newExecs {
		base := basename(exec.Path)
		newByBasename[base] = append(newByBasename[base], exec)
	}

	// Find matched, added, and removed by basename.
	for base, newList := range newByBasename {
		oldList, exists := oldByBasename[base]
		if !exists {
			// Truly new executables (basename not in old).
			diff.Executables.Added = append(diff.Executables.Added, newList...)
		} else if len(oldList) == 1 && len(newList) == 1 {
			// Single match - pair them up.
			if oldList[0].Path != newList[0].Path {
				diff.Executables.Matched = append(diff.Executables.Matched, ExecutableMatch{
					Basename: base,
					Old:      oldList[0],
					New:      newList[0],
				})
			}
			// If paths are identical, no change to report.
		}
		// Multiple matches are ambiguous - skip for now.
	}
	for base, oldList := range oldByBasename {
		if _, exists := newByBasename[base]; !exists {
			// Truly removed executables (basename not in new).
			diff.Executables.Removed = append(diff.Executables.Removed, oldList...)
		}
	}

	// Compare network.
	for key, newNet := range newNetwork {
		if _, exists := oldNetwork[key]; !exists {
			diff.Network.Added = append(diff.Network.Added, newNet)
		}
	}
	for key, oldNet := range oldNetwork {
		if _, exists := newNetwork[key]; !exists {
			diff.Network.Removed = append(diff.Network.Removed, oldNet)
		}
	}

	return nil
}

// collectExecutablesAndNetwork extracts executables and network addresses from an action.
func collectExecutablesAndNetwork(
	action *sgpb.Action,
	resources map[pbdigest.Digest]*sgpb.Resource,
	execs map[string]ExecutableChange,
	network map[string]NetworkChange,
) {
	// Collect executable.
	if action.GetExecutableResourceDigest() != "" {
		digest, err := pbdigest.NewFromString(action.GetExecutableResourceDigest())
		if err == nil {
			if res, ok := resources[digest]; ok && res.GetFileInfo() != nil {
				path := res.GetFileInfo().GetPath()
				if path != "" {
					var argv []string
					if action.GetExecInfo() != nil {
						argv = action.GetExecInfo().GetArgv()
					}
					execs[path] = ExecutableChange{
						Path:     path,
						Digest:   digest,
						ActionID: action.GetId(),
						Argv:     argv,
					}
				}
			}
		}
	}

	// Collect network addresses from inputs and outputs.
	for digestStr := range action.GetInputs() {
		collectNetworkFromDigest(digestStr, resources, network, action.GetId())
	}
	for digestStr := range action.GetOutputs() {
		collectNetworkFromDigest(digestStr, resources, network, action.GetId())
	}
}

func collectNetworkFromDigest(digestStr string, resources map[pbdigest.Digest]*sgpb.Resource, network map[string]NetworkChange, actionID int64) {
	digest, err := pbdigest.NewFromString(digestStr)
	if err != nil {
		return
	}
	res, ok := resources[digest]
	if !ok || res.GetType() != sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS {
		return
	}
	netInfo := res.GetNetworkAddrInfo()
	if netInfo == nil {
		return
	}
	key := netInfo.GetProtocol() + "://" + netInfo.GetAddress()
	network[key] = NetworkChange{
		Protocol: netInfo.GetProtocol(),
		Address:  netInfo.GetAddress(),
		ActionID: actionID,
	}
}

// diffFiles compares file resources between graphs.
func diffFiles(
	oldResources, newResources map[pbdigest.Digest]*sgpb.Resource,
	diff *SysGraphDiff,
	opts Options,
) {
	// Build path -> digest maps for files.
	oldFiles := make(map[string]pbdigest.Digest) // path -> digest
	newFiles := make(map[string]pbdigest.Digest)
	oldFileRes := make(map[string]*sgpb.Resource)
	newFileRes := make(map[string]*sgpb.Resource)

	for digest, res := range oldResources {
		if res.GetType() == sgpb.ResourceType_RESOURCE_TYPE_FILE && res.GetFileInfo() != nil {
			path := res.GetFileInfo().GetPath()
			if path != "" {
				oldFiles[path] = digest
				oldFileRes[path] = res
			}
		}
	}
	for digest, res := range newResources {
		if res.GetType() == sgpb.ResourceType_RESOURCE_TYPE_FILE && res.GetFileInfo() != nil {
			path := res.GetFileInfo().GetPath()
			if path != "" {
				newFiles[path] = digest
				newFileRes[path] = res
			}
		}
	}

	// Find added files.
	for path, newDigest := range newFiles {
		if _, exists := oldFiles[path]; !exists {
			diff.Files.Added = append(diff.Files.Added, FileChange{
				Path:      path,
				NewDigest: newDigest,
			})
		}
	}

	// Find removed files.
	for path, oldDigest := range oldFiles {
		if _, exists := newFiles[path]; !exists {
			diff.Files.Removed = append(diff.Files.Removed, FileChange{
				Path:      path,
				OldDigest: oldDigest,
			})
		}
	}

	// Find changed files.
	for path, oldDigest := range oldFiles {
		newDigest, exists := newFiles[path]
		if !exists {
			continue
		}
		if oldDigest != newDigest {
			change := FileChange{
				Path:      path,
				OldDigest: oldDigest,
				NewDigest: newDigest,
				SizeDelta: newDigest.Size - oldDigest.Size,
			}

			// Check normalization rules.
			normalized := false
			for _, rule := range opts.NormalizationRules {
				if rule.ShouldNormalize(oldFileRes[path], newFileRes[path]) {
					change.NormalizeReason = rule.Description()
					diff.Files.Normalized = append(diff.Files.Normalized, change)
					diff.NormalizedCounts[rule.Description()]++
					normalized = true
					break
				}
			}
			if !normalized {
				diff.Files.Changed = append(diff.Files.Changed, change)
			}
		}
	}
}

// diffStructure compares process tree structure using normalized basenames.
func diffStructure(ctx context.Context, old, new sgtransform.SysGraph, diff *SysGraphDiff) error {
	// Collect edges by basename pairs, with examples.
	oldEdges := make(map[string]*TreeEdge) // "parentBase->childBase" -> edge with examples
	newEdges := make(map[string]*TreeEdge)

	// Collect from old graph.
	oldResources, _ := old.Resources(ctx)
	oldActionIDs, err := old.ActionIDs(ctx)
	if err != nil {
		return err
	}
	for _, aid := range oldActionIDs {
		action, err := old.Action(ctx, aid)
		if err != nil {
			return err
		}
		collectEdgesByBasename(ctx, old, action, oldResources, oldEdges)
	}

	// Collect from new graph.
	newResources, _ := new.Resources(ctx)
	newActionIDs, err := new.ActionIDs(ctx)
	if err != nil {
		return err
	}
	for _, aid := range newActionIDs {
		action, err := new.Action(ctx, aid)
		if err != nil {
			return err
		}
		collectEdgesByBasename(ctx, new, action, newResources, newEdges)
	}

	// Compare edges by basename.
	for key, edge := range newEdges {
		if _, exists := oldEdges[key]; !exists {
			diff.Structure.NewEdges = append(diff.Structure.NewEdges, *edge)
		} else {
			diff.Structure.CommonEdgeCount++
		}
	}
	for key, edge := range oldEdges {
		if _, exists := newEdges[key]; !exists {
			diff.Structure.RemovedEdges = append(diff.Structure.RemovedEdges, *edge)
		}
	}

	return nil
}

// collectEdgesByBasename collects parent->child edges normalized by basename.
func collectEdgesByBasename(ctx context.Context, sg sgtransform.SysGraph, action *sgpb.Action, resources map[pbdigest.Digest]*sgpb.Resource, edges map[string]*TreeEdge) {
	parentExe := getExecutablePath(action, resources)
	if parentExe == "" {
		return
	}
	parentBase := basename(parentExe)

	for childID := range action.GetChildren() {
		childAction, err := sg.Action(ctx, childID)
		if err != nil {
			continue
		}
		childExe := getExecutablePath(childAction, resources)
		if childExe == "" {
			continue
		}
		childBase := basename(childExe)

		// Skip self-edges (forks) - they're usually noise.
		if parentBase == childBase {
			continue
		}

		// Key by basename pair.
		key := parentBase + "->" + childBase

		var argv []string
		if childAction.GetExecInfo() != nil {
			argv = childAction.GetExecInfo().GetArgv()
		}

		spawn := ProcessSpawn{
			ParentActionID: action.GetId(),
			ChildActionID:  childID,
			ParentExe:      parentExe,
			ChildExe:       childExe,
			Argv:           argv,
		}

		if edge, exists := edges[key]; exists {
			// Add as example if we don't have too many.
			if len(edge.Examples) < 3 {
				edge.Examples = append(edge.Examples, spawn)
			}
		} else {
			edges[key] = &TreeEdge{
				ParentBasename: parentBase,
				ChildBasename:  childBase,
				Examples:       []ProcessSpawn{spawn},
			}
		}
	}
}

// basename extracts the base name from a path.
func basename(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func getExecutablePath(action *sgpb.Action, resources map[pbdigest.Digest]*sgpb.Resource) string {
	digestStr := action.GetExecutableResourceDigest()
	if digestStr == "" {
		return ""
	}
	digest, err := pbdigest.NewFromString(digestStr)
	if err != nil {
		return ""
	}
	res, ok := resources[digest]
	if !ok || res.GetFileInfo() == nil {
		return ""
	}
	return res.GetFileInfo().GetPath()
}

// generateSecurityAlerts creates security alerts from the diff.
func generateSecurityAlerts(diff *SysGraphDiff) {
	// Alert on new executables in suspicious locations.
	for _, exec := range diff.Executables.Added {
		if isSuspiciousPath(exec.Path) {
			diff.SecurityAlerts = append(diff.SecurityAlerts, SecurityAlert{
				Severity:    "warning",
				Category:    "executable",
				Description: "New executable in suspicious location: " + exec.Path,
				ActionID:    exec.ActionID,
				Details: map[string]string{
					"path": exec.Path,
				},
			})
		}

		// Check for shell pipe patterns.
		if hasShellPipePattern(exec.Argv) {
			diff.SecurityAlerts = append(diff.SecurityAlerts, SecurityAlert{
				Severity:    "critical",
				Category:    "script",
				Description: "Shell pipe pattern detected: " + strings.Join(exec.Argv, " "),
				ActionID:    exec.ActionID,
				Details: map[string]string{
					"argv": strings.Join(exec.Argv, " "),
				},
			})
		}
	}

	// Alert on all new network connections.
	for _, net := range diff.Network.Added {
		diff.SecurityAlerts = append(diff.SecurityAlerts, SecurityAlert{
			Severity:    "info",
			Category:    "network",
			Description: "New network connection: " + net.Protocol + "://" + net.Address,
			ActionID:    net.ActionID,
			Details: map[string]string{
				"protocol": net.Protocol,
				"address":  net.Address,
			},
		})
	}
}

func isSuspiciousPath(path string) bool {
	suspiciousPrefixes := []string{
		"/tmp/",
		"/var/tmp/",
		"/dev/shm/",
	}
	for _, prefix := range suspiciousPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func hasShellPipePattern(argv []string) bool {
	argStr := strings.Join(argv, " ")
	patterns := []string{
		"| sh",
		"| bash",
		"| /bin/sh",
		"| /bin/bash",
		"|sh",
		"|bash",
		"eval ",
	}
	for _, pattern := range patterns {
		if strings.Contains(argStr, pattern) {
			return true
		}
	}
	return false
}

// HasChanges returns true if there are any differences.
func (d *SysGraphDiff) HasChanges() bool {
	return len(d.SecurityAlerts) > 0 ||
		len(d.Executables.Added) > 0 ||
		len(d.Executables.Removed) > 0 ||
		len(d.Executables.Changed) > 0 ||
		len(d.Network.Added) > 0 ||
		len(d.Network.Removed) > 0 ||
		len(d.Files.Added) > 0 ||
		len(d.Files.Removed) > 0 ||
		len(d.Files.Changed) > 0 ||
		len(d.Structure.NewEdges) > 0 ||
		len(d.Structure.RemovedEdges) > 0
}

// Summary returns a brief summary of changes.
func (d *SysGraphDiff) Summary() string {
	parts := []string{}

	if n := len(d.Executables.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("+%d executables", n))
	}
	if n := len(d.Executables.Removed); n > 0 {
		parts = append(parts, fmt.Sprintf("-%d executables", n))
	}
	if n := len(d.Network.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("+%d network", n))
	}
	if n := len(d.Files.Changed); n > 0 {
		parts = append(parts, fmt.Sprintf("~%d files", n))
	}
	if n := len(d.Files.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("+%d files", n))
	}
	if n := len(d.Files.Removed); n > 0 {
		parts = append(parts, fmt.Sprintf("-%d files", n))
	}

	if len(parts) == 0 {
		return "No changes"
	}
	return strings.Join(parts, ", ")
}
