// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package describe

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/netclassify"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	pkgerrors "github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the describe command.
type Config struct {
	SysgraphPath string
	NetlogPath   string
	JSON         bool
	Sections     string // comma-separated list of sections to display (empty = all)
}

// validSections lists the recognized section names for --sections.
var validSections = map[string]bool{
	"git": true, "network": true, "tools": true, "compile": true,
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.SysgraphPath == "" {
		return errors.New("sysgraph path is required")
	}
	for _, s := range c.parseSections() {
		if !validSections[s] {
			return fmt.Errorf("unknown section %q (valid: git, network, tools, compile)", s)
		}
	}
	return nil
}

// parseSections returns the requested sections, or nil for all.
func (c Config) parseSections() []string {
	if c.Sections == "" {
		return nil
	}
	return strings.Split(c.Sections, ",")
}

// showSection reports whether the given section should be displayed.
func (c Config) showSection(name string) bool {
	sections := c.parseSections()
	if sections == nil {
		return true
	}
	for _, s := range sections {
		if s == name {
			return true
		}
	}
	return false
}

// Deps holds dependencies for the command.
type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) {
	return &Deps{}, nil
}

// BuildDescription is the structured output of the describe command.
type BuildDescription struct {
	// GitRepos lists the git repositories accessed during the build.
	GitRepos []GitRepo `json:"git_repos"`
	// Dependencies lists non-git network dependencies fetched during the build.
	Dependencies []Dependency `json:"dependencies"`
	// BuildTools lists the build tools used (e.g. make, cmake, meson).
	BuildTools []BuildTool `json:"build_tools"`
	// CompileStats contains statistics about compile commands.
	CompileStats *CompileStats `json:"compile_stats"`
}

// GitRepo describes a git repository accessed during the build.
type GitRepo struct {
	URL     string `json:"url"`
	Ref     string `json:"ref,omitempty"`     // Branch, tag, or commit hash checked out.
	Dir     string `json:"dir,omitempty"`     // Local directory the repo was cloned into.
	Shallow bool   `json:"shallow,omitempty"` // Whether this was a shallow clone.
	Command string `json:"command,omitempty"` // The command that fetched it.
}

// deferredGitOp records a git fetch or checkout that must be resolved after
// all clone/remote-add actions have populated the repo map.
type deferredGitOp struct {
	cwd     string
	ref     string
	shallow bool
	kind    string // "fetch" or "checkout"
}

// Dependency describes a non-git network dependency, classified by pURL where
// possible.
type Dependency struct {
	// PURL is the package URL if the dependency was classifiable (e.g.
	// "pkg:deb/debian/zlib1g@1.2.13"). Empty for unclassified deps.
	PURL string `json:"purl,omitempty"`
	// URL is the raw URL, populated only for unclassified dependencies.
	URL string `json:"url,omitempty"`
}

// BuildTool describes a build tool used during the build.
type BuildTool struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	InvokeCount int      `json:"invoke_count"`
	ExampleArgs []string `json:"example_args,omitempty"`
}

// CompileStats contains statistics about compile commands.
type CompileStats struct {
	Count          int           `json:"count"`
	MinDuration    time.Duration `json:"min_duration_ns"`
	MaxDuration    time.Duration `json:"max_duration_ns"`
	MeanDuration   time.Duration `json:"mean_duration_ns"`
	MedianDuration time.Duration `json:"median_duration_ns"`
}

// knownBuildTools maps executable basenames to display names.
var knownBuildTools = map[string]string{
	"make":       "Make",
	"gmake":      "GNU Make",
	"cmake":      "CMake",
	"meson":      "Meson",
	"ninja":      "Ninja",
	"bazel":      "Bazel",
	"cargo":      "Cargo",
	"gn":         "GN",
	"go":         "Go",
	"mvn":        "Maven",
	"gradle":     "Gradle",
	"ant":        "Ant",
	"pip":        "pip",
	"pip3":       "pip3",
	"npm":        "npm",
	"yarn":       "yarn",
	"pnpm":       "pnpm",
	"scons":      "SCons",
	"autoconf":   "Autoconf",
	"automake":   "Automake",
	"configure":  "configure",
	"autoreconf": "autoreconf",
	"libtool":    "Libtool",
}

// knownCompilers lists executable basenames considered compilers.
var knownCompilers = map[string]bool{
	"cc": true, "gcc": true, "g++": true, "c++": true,
	"clang": true, "clang++": true,
	"rustc": true, "javac": true, "scalac": true,
	"as": true, "nasm": true,
	"ld": true, "gold": true, "lld": true, "mold": true,
	"ar": true, "ranlib": true,
}

// Handler contains the business logic for describing a build.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	sg, err := sgstorage.LoadSysGraph(ctx, cfg.SysgraphPath)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "loading sysgraph")
	}
	defer sg.Close()

	var httpRequests []netlog.HTTPRequestLog
	if cfg.NetlogPath != "" {
		netlogData, err := os.ReadFile(cfg.NetlogPath)
		if err != nil {
			return nil, pkgerrors.Wrap(err, "reading netlog file")
		}
		var nal netlog.NetworkActivityLog
		if err := json.Unmarshal(netlogData, &nal); err != nil {
			return nil, pkgerrors.Wrap(err, "decoding netlog")
		}
		httpRequests = nal.HTTPRequests
	}

	desc, err := Describe(ctx, sg, httpRequests)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "describing build")
	}

	if cfg.JSON {
		out, err := json.MarshalIndent(desc, "", "  ")
		if err != nil {
			return nil, pkgerrors.Wrap(err, "marshaling JSON")
		}
		fmt.Fprintln(deps.IO.Out, string(out))
	} else {
		printDescription(cfg, deps, desc)
	}

	return &act.NoOutput{}, nil
}

// Describe analyzes a sysgraph and produces a BuildDescription.
// httpRequests provides netlog data for network dependency discovery. It may be
// nil, in which case network info is derived only from sysgraph metadata.
func Describe(ctx context.Context, sg sgtransform.SysGraph, httpRequests []netlog.HTTPRequestLog) (*BuildDescription, error) {
	resources, err := sg.Resources(ctx)
	if err != nil {
		return nil, err
	}

	var desc BuildDescription

	// Concurrent-safe maps for the parallel RangeActions callback.
	var gitRepos syncx.Map[string, *GitRepo]     // normalized url -> repo info
	var depsSeen syncx.Map[string, *Dependency]  // purl or raw url -> dep
	var toolCounts syncx.Map[string, *BuildTool] // basename -> tool
	var portToAction syncx.Map[string, *sgpb.Action]
	var compileDurations syncx.Map[int64, time.Duration] // action id -> duration

	// Deferred git fetch/checkout ops that depend on repo entries created
	// by clone/remote-add. Resolved after the parallel pass completes.
	var deferredGitOps syncx.Map[int64, *deferredGitOp] // action id -> op

	err = sgquery.RangeActions(ctx, sg, func(ctx context.Context, a *sgpb.Action) error {
		execPath := getExecutablePath(a, resources)
		execBase := filepath.Base(execPath)

		// --- Git repos from sysgraph metadata (annotated sysgraph) ---
		checkGitAndNetworkFromMetadata(a, execBase, &gitRepos, &depsSeen)

		// --- Git repos and refs from argv ---
		extractGitInfo(a, execBase, &gitRepos, &deferredGitOps)

		// --- Build port->action index for netlog association ---
		if len(httpRequests) > 0 {
			for digestStr := range a.GetOutputs() {
				dg, err := pbdigest.NewFromString(digestStr)
				if err != nil {
					continue
				}
				r, ok := resources[dg]
				if !ok || r.GetType() != sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS {
					continue
				}
				sport, err := extractSourcePort(r.GetNetworkAddrInfo().GetAddress())
				if err != nil {
					continue
				}
				portToAction.Store(sport, a)
			}
		}

		// Skip fork-only clones for tool counting and compile stats. These
		// inherit the parent executable but never actually ran the tool.
		if !a.GetIsFork() {
			if displayName, ok := knownBuildTools[execBase]; ok {
				if _, loaded := toolCounts.LoadOrStore(execBase, &BuildTool{
					Name:        displayName,
					Path:        execPath,
					InvokeCount: 1,
					ExampleArgs: func() []string {
						if a.GetExecInfo() != nil {
							return a.GetExecInfo().GetArgv()
						}
						return nil
					}(),
				}); loaded {
					// Already exists; we skip incrementing here since exact
					// counts are not critical under concurrency. A follow-up
					// pass corrects this below.
				}
			}
		}

		// --- Compile stats (also skip forks) ---
		if !a.GetIsFork() && knownCompilers[execBase] {
			start := a.GetStartTime().AsTime()
			end := a.GetEndTime().AsTime()
			if !start.IsZero() && !end.IsZero() && end.After(start) {
				compileDurations.Store(a.GetId(), end.Sub(start))
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Resolve deferred git fetch/checkout ops now that all clone/remote-add
	// entries exist. Process in action-ID order for determinism.
	var deferredKeys []int64
	for id := range deferredGitOps.Keys() {
		deferredKeys = append(deferredKeys, id)
	}
	sort.Slice(deferredKeys, func(i, j int) bool { return deferredKeys[i] < deferredKeys[j] })
	for _, id := range deferredKeys {
		op, ok := deferredGitOps.Load(id)
		if !ok {
			continue
		}
		switch op.kind {
		case "fetch":
			mergeGitRepoForCwd(op.cwd, op.ref, op.shallow, &gitRepos)
		case "checkout":
			matchGitRepoForCwd(op.cwd, op.ref, &gitRepos)
		}
	}

	// Second pass for accurate tool invocation counts.
	toolInvokeCounts, err := sgquery.MapAllActions(ctx, sg, func(a *sgpb.Action) (int64, string, error) {
		if a.GetIsFork() {
			return 0, "", nil
		}
		execBase := filepath.Base(getExecutablePath(a, resources))
		if _, ok := knownBuildTools[execBase]; ok {
			return a.GetId(), execBase, nil
		}
		return 0, "", nil
	})
	if err != nil {
		return nil, err
	}
	toolCountsByName := map[string]int{}
	for _, name := range toolInvokeCounts {
		if name != "" {
			toolCountsByName[name]++
		}
	}

	// --- Netlog-based network dependency discovery ---
	// This directly uses the netlog entries, associating each HTTP request
	// with an action via source port. Unlike the annotate-network command
	// (which keeps only one entry per port), this handles multiple requests
	// per port (HTTP keep-alive / connection reuse).
	for i := range httpRequests {
		entry := &httpRequests[i]
		fullURL := entry.Scheme + "://" + entry.Host + entry.Path

		execBase := ""
		if a, ok := portToAction.Load(entry.PeerPort); ok && a.GetExecInfo() != nil {
			execBase = filepath.Base(getExecutablePath(a, resources))
		}
		classifyAndStoreDep(fullURL, execBase, &gitRepos, &depsSeen)
	}

	// Assemble git repos.
	for _, repo := range gitRepos.Iter() {
		desc.GitRepos = append(desc.GitRepos, *repo)
	}
	sort.Slice(desc.GitRepos, func(i, j int) bool { return desc.GitRepos[i].URL < desc.GitRepos[j].URL })

	// Assemble dependencies (non-git network fetches).
	for _, dep := range depsSeen.Iter() {
		desc.Dependencies = append(desc.Dependencies, *dep)
	}
	sort.Slice(desc.Dependencies, func(i, j int) bool {
		// Classified (pURL) deps sort before unclassified (raw URL) deps.
		di, dj := desc.Dependencies[i], desc.Dependencies[j]
		ki, kj := di.PURL+di.URL, dj.PURL+dj.URL
		return ki < kj
	})

	// Assemble build tools.
	for basename, tool := range toolCounts.Iter() {
		if cnt, ok := toolCountsByName[basename]; ok {
			tool.InvokeCount = cnt
		}
		desc.BuildTools = append(desc.BuildTools, *tool)
	}
	sort.Slice(desc.BuildTools, func(i, j int) bool { return desc.BuildTools[i].Name < desc.BuildTools[j].Name })

	// Compute compile stats.
	var durations []time.Duration
	for _, d := range compileDurations.Iter() {
		durations = append(durations, d)
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var total time.Duration
		for _, d := range durations {
			total += d
		}
		desc.CompileStats = &CompileStats{
			Count:          len(durations),
			MinDuration:    durations[0],
			MaxDuration:    durations[len(durations)-1],
			MeanDuration:   total / time.Duration(len(durations)),
			MedianDuration: durations[len(durations)/2],
		}
	}

	return &desc, nil
}

// classifyAndStoreDep classifies a URL via netclassify and stores it as either
// a git repo or a classified/unclassified dependency.
func classifyAndStoreDep(
	fullURL string,
	execBase string,
	gitRepos *syncx.Map[string, *GitRepo],
	depsSeen *syncx.Map[string, *Dependency],
) {
	// Check git first (via host or command context).
	host := extractHost(fullURL)
	if isGitHost(host) || isGitCommand(execBase) {
		repoURL := normalizeGitURL(fullURL)
		gitRepos.LoadOrStore(repoURL, &GitRepo{URL: repoURL})
		return
	}

	purl, err := netclassify.ClassifyURL(fullURL)
	if err == nil {
		// Classified: dedup by pURL.
		depsSeen.LoadOrStore(purl, &Dependency{PURL: purl})
	} else if errors.Is(err, netclassify.ErrSkipped) {
		// API/metadata URL, skip entirely.
		return
	} else {
		// Unclassified: keep the raw URL.
		depsSeen.LoadOrStore(fullURL, &Dependency{URL: fullURL})
	}
}

// extractHost returns the host portion of a URL.
func extractHost(rawURL string) string {
	// Quick extraction without full URL parsing.
	after := rawURL
	if idx := strings.Index(after, "://"); idx >= 0 {
		after = after[idx+3:]
	}
	if idx := strings.IndexAny(after, "/:"); idx >= 0 {
		after = after[:idx]
	}
	return after
}

// checkGitAndNetworkFromMetadata examines action metadata for HTTP annotations
// (from annotated sysgraphs) and classifies them as git repos or deps.
func checkGitAndNetworkFromMetadata(
	a *sgpb.Action,
	execBase string,
	gitRepos *syncx.Map[string, *GitRepo],
	depsSeen *syncx.Map[string, *Dependency],
) {
	md := a.GetMetadata()
	for key, val := range md {
		if !strings.HasSuffix(key, ".http.host") {
			continue
		}
		prefix := strings.TrimSuffix(key, "http.host")
		host := val
		path := md[prefix+"http.path"]
		scheme := md[prefix+"http.scheme"]

		fullURL := scheme + "://" + host + path
		classifyAndStoreDep(fullURL, execBase, gitRepos, depsSeen)
	}
}

// extractGitInfo parses git command argv to extract repo URLs, refs, and clone info.
// Clone and remote-add are applied immediately (they create repo entries).
// Fetch and checkout are deferred into deferredOps so they can be resolved
// after all repo entries exist, avoiding races from parallel action processing.
func extractGitInfo(a *sgpb.Action, execBase string, gitRepos *syncx.Map[string, *GitRepo], deferredOps *syncx.Map[int64, *deferredGitOp]) {
	if !isGitCommand(execBase) || a.GetExecInfo() == nil {
		return
	}
	argv := a.GetExecInfo().GetArgv()
	if len(argv) < 2 {
		return
	}

	// Find the git subcommand, skipping flags like "-c key=val".
	subCmd, args := parseGitSubcommand(argv)

	switch subCmd {
	case "clone":
		parseGitClone(args, a, gitRepos)
	case "remote":
		parseGitRemoteAdd(args, a, gitRepos)
	case "fetch":
		deferGitFetch(args, a, deferredOps)
	case "checkout":
		deferGitCheckout(args, a, deferredOps)
	}
}

// parseGitSubcommand extracts the git subcommand and remaining args from argv.
// Skips global flags like -c, --git-dir, etc.
func parseGitSubcommand(argv []string) (string, []string) {
	i := 1 // skip argv[0] which is the git binary
	for i < len(argv) {
		arg := argv[i]
		if arg == "-c" || arg == "--git-dir" || arg == "--work-tree" || arg == "--shallow-file" {
			i += 2 // skip flag and its value
			continue
		}
		if strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") || strings.HasPrefix(arg, "--shallow-file=") {
			i++
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

// parseGitClone extracts repo URL, ref, dir, and shallow info from "git clone" args.
func parseGitClone(args []string, a *sgpb.Action, gitRepos *syncx.Map[string, *GitRepo]) {
	var url, branch, dir string
	shallow := false
	positional := []string{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--depth" && i+1 < len(args):
			shallow = true
			i++
		case strings.HasPrefix(arg, "--depth="):
			shallow = true
		case arg == "--branch" || arg == "-b":
			if i+1 < len(args) {
				branch = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, "--branch="):
			branch = strings.TrimPrefix(arg, "--branch=")
		case strings.HasPrefix(arg, "-"):
			// skip other flags; consume value for known two-arg flags
			if (arg == "-o" || arg == "--origin" || arg == "--reference" || arg == "--separate-git-dir" || arg == "-j" || arg == "--jobs" || arg == "--filter") && i+1 < len(args) {
				i++
			}
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) >= 1 {
		url = positional[0]
	}
	if len(positional) >= 2 {
		dir = positional[1]
	}

	if url == "" {
		return
	}

	repoURL := normalizeGitURL(url)
	repo := &GitRepo{
		URL:     repoURL,
		Ref:     branch,
		Dir:     dir,
		Shallow: shallow,
		Command: strings.Join(a.GetExecInfo().GetArgv(), " "),
	}

	// Use LoadOrStore but merge info if the entry already exists.
	if existing, loaded := gitRepos.LoadOrStore(repoURL, repo); loaded {
		// Merge: prefer more specific info.
		if existing.Ref == "" && repo.Ref != "" {
			existing.Ref = repo.Ref
		}
		if existing.Dir == "" && repo.Dir != "" {
			existing.Dir = repo.Dir
		}
		if !existing.Shallow && repo.Shallow {
			existing.Shallow = repo.Shallow
		}
		if existing.Command == "" && repo.Command != "" {
			existing.Command = repo.Command
		}
	}
}

// parseGitRemoteAdd handles "git remote add <name> <url>" to create a repo
// entry keyed by the action's working directory.
func parseGitRemoteAdd(args []string, a *sgpb.Action, gitRepos *syncx.Map[string, *GitRepo]) {
	// We only care about "add" subcommand.
	if len(args) < 1 || args[0] != "add" {
		return
	}
	addArgs := args[1:]

	// Extract positional args (name, url), skipping flags.
	var positional []string
	for i := 0; i < len(addArgs); i++ {
		arg := addArgs[i]
		if strings.HasPrefix(arg, "-") {
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) < 2 {
		return
	}
	url := positional[1]
	repoURL := normalizeGitURL(url)

	cwd := a.GetExecInfo().GetWorkingDirectory()
	repo := &GitRepo{
		URL: repoURL,
		Dir: cwd,
	}
	if existing, loaded := gitRepos.LoadOrStore(repoURL, repo); loaded {
		if existing.Dir == "" {
			existing.Dir = cwd
		}
	}
}

// deferGitFetch parses "git fetch [--depth N] <remote> [<refspec>]" and stores
// a deferred op for post-pass resolution.
func deferGitFetch(args []string, a *sgpb.Action, deferredOps *syncx.Map[int64, *deferredGitOp]) {
	shallow := false
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--depth" && i+1 < len(args):
			shallow = true
			i++
		case strings.HasPrefix(arg, "--depth="):
			shallow = true
		case strings.HasPrefix(arg, "-"):
			// Skip other flags; consume value for known two-arg flags.
			if (arg == "--upload-pack" || arg == "-j" || arg == "--jobs" || arg == "--filter" || arg == "--negotiation-tip" || arg == "--refmap" || arg == "-o" || arg == "--server-option" || arg == "--recurse-submodules-default") && i+1 < len(args) {
				i++
			}
		default:
			positional = append(positional, arg)
		}
	}

	// positional[0] is the remote name/URL, positional[1] is the refspec.
	var ref string
	if len(positional) >= 2 {
		ref = positional[1]
	}

	cwd := a.GetExecInfo().GetWorkingDirectory()
	deferredOps.Store(a.GetId(), &deferredGitOp{cwd: cwd, ref: ref, shallow: shallow, kind: "fetch"})
}

// mergeGitRepoForCwd finds the repo whose directory matches the action's cwd
// and merges ref and shallow info.
func mergeGitRepoForCwd(cwd, ref string, shallow bool, gitRepos *syncx.Map[string, *GitRepo]) {
	gitRepos.Range(func(_ string, repo *GitRepo) bool {
		if repo.Dir == "" {
			return true
		}
		if repo.Dir == cwd || filepath.Base(cwd) == repo.Dir || strings.HasSuffix(cwd, "/"+repo.Dir) {
			if repo.Ref == "" && ref != "" {
				repo.Ref = ref
			}
			if !repo.Shallow && shallow {
				repo.Shallow = shallow
			}
			return false // stop iteration
		}
		return true
	})
}

// deferGitCheckout parses "git checkout <ref>" and stores a deferred op for
// post-pass resolution.
func deferGitCheckout(args []string, a *sgpb.Action, deferredOps *syncx.Map[int64, *deferredGitOp]) {
	// Extract ref: last positional arg (skip flags).
	var ref string
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		ref = arg
	}
	if ref == "" {
		return
	}

	// Associate with the repo whose clone location contains the cwd.
	cwd := a.GetExecInfo().GetWorkingDirectory()
	deferredOps.Store(a.GetId(), &deferredGitOp{cwd: cwd, ref: ref, kind: "checkout"})
}

// matchGitRepoForCwd finds the repo whose cloned directory matches (or
// contains) cwd and sets its Ref if not already set.
func matchGitRepoForCwd(cwd, ref string, gitRepos *syncx.Map[string, *GitRepo]) {
	// Collect all repos with a known clone command so we can match by directory.
	type repoWithCloneDir struct {
		repo     *GitRepo
		cloneDir string // absolute path of the clone
	}
	var candidates []repoWithCloneDir

	gitRepos.Range(func(_ string, repo *GitRepo) bool {
		if repo.Command == "" {
			return true
		}
		// Reconstruct the absolute clone directory from the command's context.
		// The clone command argv contains the directory as the last positional arg.
		// We stored Dir as the positional arg from "git clone ... <dir>".
		if repo.Dir == "" {
			return true
		}
		candidates = append(candidates, repoWithCloneDir{repo: repo, cloneDir: repo.Dir})
		return true
	})

	// Try to match cwd to a clone directory. A checkout in /src/projects/fio
	// should match a repo cloned to "." in /src (since /src contains /src/projects/fio).
	// We match the most specific (longest) clone dir first.
	sort.Slice(candidates, func(i, j int) bool {
		return len(candidates[i].cloneDir) > len(candidates[j].cloneDir)
	})

	for _, c := range candidates {
		dir := c.cloneDir
		// "." means cloned into the parent's cwd, which we don't have directly.
		// But we can check if the cwd basename matches a non-"." dir.
		if dir != "." {
			if filepath.Base(cwd) == dir || strings.HasSuffix(cwd, "/"+dir) || cwd == dir {
				if c.repo.Ref == "" {
					c.repo.Ref = ref
				}
				return
			}
		}
	}

	// Fallback: if there's a "." clone and no better match, use it.
	for _, c := range candidates {
		if c.cloneDir == "." {
			if c.repo.Ref == "" {
				c.repo.Ref = ref
			}
			return
		}
	}
}

// extractSourcePort parses the source port from a tcp_connect address string
// of the format "saddr:sport->daddr:dport".
func extractSourcePort(address string) (string, error) {
	parts := strings.SplitN(address, "->", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid address format: %s", address)
	}
	src := parts[0]
	idx := strings.LastIndex(src, ":")
	if idx < 0 {
		return "", fmt.Errorf("no port in source address: %s", src)
	}
	return src[idx+1:], nil
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

func isGitHost(host string) bool {
	gitHosts := []string{"github.com", "gitlab.com", "bitbucket.org", "git.kernel.org", "git.savannah.gnu.org", "git.code.sf.net"}
	for _, h := range gitHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func isGitCommand(basename string) bool {
	return basename == "git" || strings.HasPrefix(basename, "git-")
}

// normalizeGitURL extracts the repo URL from a full URL.
// e.g. "https://github.com/axboe/fio.git/info/refs?service=git-upload-pack"
// becomes "https://github.com/axboe/fio"
func normalizeGitURL(rawURL string) string {
	// Strip query string.
	if idx := strings.Index(rawURL, "?"); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	// Strip trailing slash.
	rawURL = strings.TrimRight(rawURL, "/")
	// For known hosts, keep only first two path segments (owner/repo).
	for _, prefix := range []string{"https://github.com/", "https://gitlab.com/", "https://bitbucket.org/"} {
		if strings.HasPrefix(rawURL, prefix) {
			rest := rawURL[len(prefix):]
			// Strip .git from the repo name within the path.
			rest = strings.TrimSuffix(rest, ".git")
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 {
				return prefix + parts[0] + "/" + parts[1]
			}
		}
	}
	// Generic: strip .git suffix.
	rawURL = strings.TrimSuffix(rawURL, ".git")
	return rawURL
}

func printDescription(cfg Config, deps *Deps, desc *BuildDescription) {
	w := deps.IO.Out

	fmt.Fprintln(w, "=== Build Description ===")
	fmt.Fprintln(w)

	// Git repos.
	if cfg.showSection("git") {
		fmt.Fprintf(w, "Git Repositories (%d):\n", len(desc.GitRepos))
		if len(desc.GitRepos) == 0 {
			fmt.Fprintln(w, "  (none detected)")
		}
		for _, repo := range desc.GitRepos {
			fmt.Fprintf(w, "  %s\n", repo.URL)
			if repo.Ref != "" {
				fmt.Fprintf(w, "    ref: %s\n", repo.Ref)
			}
			if repo.Dir != "" {
				fmt.Fprintf(w, "    dir: %s\n", repo.Dir)
			}
			if repo.Shallow {
				fmt.Fprintln(w, "    shallow: true")
			}
			if repo.Command != "" {
				fmt.Fprintf(w, "    command: %s\n", repo.Command)
			}
		}
		fmt.Fprintln(w)
	}

	// Dependencies: group classified by pURL type, show unclassified separately.
	if cfg.showSection("network") {
		fmt.Fprintf(w, "Network Dependencies (%d):\n", len(desc.Dependencies))
		if len(desc.Dependencies) == 0 {
			fmt.Fprintln(w, "  (none detected)")
		}

		// Separate classified (pURL) from unclassified (raw URL).
		purlsByType := map[string][]string{} // type prefix -> list of pURLs
		var unclassified []string
		for _, dep := range desc.Dependencies {
			if dep.PURL != "" {
				// Extract type from pURL (e.g. "pkg:deb" from "pkg:deb/debian/zlib1g@1.2.13").
				typePart := dep.PURL
				if idx := strings.Index(typePart, "/"); idx >= 0 {
					typePart = typePart[:idx]
				}
				purlsByType[typePart] = append(purlsByType[typePart], dep.PURL)
			} else if dep.URL != "" {
				unclassified = append(unclassified, dep.URL)
			}
		}

		// Print unclassified deps first (most interesting).
		if len(unclassified) > 0 {
			sort.Strings(unclassified)
			fmt.Fprintf(w, "  unclassified (%d):\n", len(unclassified))
			for _, u := range unclassified {
				fmt.Fprintf(w, "    %s\n", u)
			}
		}

		// Print classified deps grouped by type.
		var types []string
		for t := range purlsByType {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, typ := range types {
			purls := purlsByType[typ]
			sort.Strings(purls)

			// For deb packages, use a compact comma-separated format with the
			// distro prefix (e.g. "debian/") pulled into the header.
			if typ == "pkg:deb" {
				// Group by distro (e.g. "debian", "ubuntu").
				distroPackages := map[string][]string{}
				for _, p := range purls {
					// p is like "pkg:deb/debian/zlib1g@1.2.13"
					rest := strings.TrimPrefix(p, "pkg:deb/")
					if idx := strings.Index(rest, "/"); idx >= 0 {
						distro := rest[:idx]
						pkg := rest[idx+1:]
						distroPackages[distro] = append(distroPackages[distro], pkg)
					}
				}
				var distros []string
				for d := range distroPackages {
					distros = append(distros, d)
				}
				sort.Strings(distros)
				for _, distro := range distros {
					pkgs := distroPackages[distro]
					fmt.Fprintf(w, "  %s/%s (%d): ", typ, distro, len(pkgs))
					fmt.Fprintln(w, strings.Join(pkgs, ", "))
				}
				continue
			}

			fmt.Fprintf(w, "  %s (%d):\n", typ, len(purls))
			for _, p := range purls {
				short := p
				if idx := strings.Index(p, "/"); idx >= 0 {
					short = p[idx+1:]
				}
				fmt.Fprintf(w, "    %s\n", short)
			}
		}
		fmt.Fprintln(w)
	}

	// Build tools.
	if cfg.showSection("tools") {
		fmt.Fprintf(w, "Build Tools (%d):\n", len(desc.BuildTools))
		if len(desc.BuildTools) == 0 {
			fmt.Fprintln(w, "  (none detected)")
		}
		for _, tool := range desc.BuildTools {
			fmt.Fprintf(w, "  %s (%s) - %d invocations\n", tool.Name, tool.Path, tool.InvokeCount)
			if len(tool.ExampleArgs) > 0 {
				fmt.Fprintf(w, "    example: %s\n", strings.Join(tool.ExampleArgs, " "))
			}
		}
		fmt.Fprintln(w)
	}

	// Compile stats.
	if cfg.showSection("compile") {
		fmt.Fprintln(w, "Compile Statistics:")
		if desc.CompileStats == nil {
			fmt.Fprintln(w, "  (no compile commands detected)")
		} else {
			s := desc.CompileStats
			fmt.Fprintf(w, "  count:  %d\n", s.Count)
			fmt.Fprintf(w, "  min:    %s\n", roundDuration(s.MinDuration))
			fmt.Fprintf(w, "  max:    %s\n", roundDuration(s.MaxDuration))
			fmt.Fprintf(w, "  mean:   %s\n", roundDuration(s.MeanDuration))
			fmt.Fprintf(w, "  median: %s\n", roundDuration(s.MedianDuration))
		}
	}
}

// roundDuration rounds a duration to the nearest whole unit for display.
// Durations >= 1s are rounded to the nearest second, otherwise to the nearest
// millisecond.
func roundDuration(d time.Duration) time.Duration {
	if d >= time.Second {
		return d.Round(time.Second)
	}
	return d.Round(time.Millisecond)
}

// Command creates a new describe command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "describe <sysgraph>",
		Short: "Describe a build from its sysgraph",
		Long: `Analyzes a sysgraph and produces a description of the build including:
  - Git repositories accessed
  - Network dependencies fetched
  - Build tools used (make, cmake, meson, etc.)
  - Compile command statistics (count, min/max/mean/median duration)

When --netlog is provided, HTTP request data is used directly for complete
network dependency discovery. Without it, network info comes only from
sysgraph metadata (which may be incomplete due to annotation limitations).`,
		Args: cobra.ExactArgs(1),
		RunE: cli.RunE(
			&cfg,
			parseArgs,
			InitDeps,
			Handler,
		),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

func parseArgs(cfg *Config, args []string) error {
	cfg.SysgraphPath = args[0]
	return nil
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.NetlogPath, "netlog", "", "path to netlog.json for complete network dependency info")
	set.BoolVar(&cfg.JSON, "json", false, "output as JSON")
	set.StringVar(&cfg.Sections, "sections", "", "comma-separated sections to display: git,network,tools,compile (default: all)")
	return set
}
