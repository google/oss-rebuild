// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgquery"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the tree command.
type Config struct {
	SysgraphPath string
	RootID       int64  // filter to subtree by action ID
	AncestorID   int64  // show ancestor path to this action ID
	MaxDepth     int    // limit recursion (0 = unlimited)
	Collapse     int    // group N+ siblings with same exec (default 10)
	ShowForks    bool   // include fork-only actions
	ShowFiles    bool   // show file reads/writes per process
	NetlogPath   string // path to netlog.json for URL annotations
	Verbose      bool   // show ids, cwd, and duration
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.SysgraphPath == "" {
		return errors.New("sysgraph path is required")
	}
	return nil
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

// Handler contains the business logic for the tree command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	sg, err := sgstorage.LoadSysGraph(ctx, cfg.SysgraphPath)
	if err != nil {
		return nil, fmt.Errorf("loading sysgraph: %w", err)
	}
	defer sg.Close()

	var filtered sgtransform.SysGraph = sg
	if cfg.RootID != 0 {
		filtered, err = sgtransform.SubgraphForRoots(ctx, sg, []int64{cfg.RootID})
		if err != nil {
			return nil, fmt.Errorf("filtering for root: %w", err)
		}
	}

	tree, err := buildTree(ctx, filtered)
	if err != nil {
		return nil, fmt.Errorf("building tree: %w", err)
	}

	var resources map[pbdigest.Digest]*sgpb.Resource
	if cfg.ShowFiles || cfg.NetlogPath != "" {
		resources, err = filtered.Resources(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading resources: %w", err)
		}
	}

	// Build action ID -> URLs map from netlog.
	var actionURLs map[int64][]string
	if cfg.NetlogPath != "" {
		actionURLs, err = buildActionURLs(ctx, filtered, resources, cfg.NetlogPath)
		if err != nil {
			return nil, fmt.Errorf("building netlog index: %w", err)
		}
	}

	opts := renderOpts{
		MaxDepth:     cfg.MaxDepth,
		Collapse:     cfg.Collapse,
		ShowForks:    cfg.ShowForks,
		ShowIDs:      cfg.Verbose,
		ShowCwd:      cfg.Verbose,
		ShowDuration: cfg.Verbose,
		ShowFiles:    cfg.ShowFiles,
		AncestorID:   cfg.AncestorID,
		resources:    resources,
		actionURLs:   actionURLs,
	}
	renderTree(deps.IO.Out, tree, opts)
	return &act.NoOutput{}, nil
}

// Command creates a new tree command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "tree <sysgraph>",
		Short: "Show process tree trace of a build",
		Long: `Displays a bash-like process tree trace of the build, showing what
commands were executed and in what order. Each nesting level is indicated
by a '+' prefix (similar to bash set -x).

Fork-only actions are collapsed by default, promoting their exec'd children
up to the parent depth. Consecutive siblings with the same executable are
grouped when there are many (controlled by --collapse).`,
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

// buildActionURLs loads a netlog file and joins HTTP requests to actions via
// source port, returning a map of action ID to the URLs it fetched.
func buildActionURLs(ctx context.Context, sg sgtransform.SysGraph, resources map[pbdigest.Digest]*sgpb.Resource, netlogPath string) (map[int64][]string, error) {
	netlogData, err := os.ReadFile(netlogPath)
	if err != nil {
		return nil, fmt.Errorf("reading netlog: %w", err)
	}
	var nal netlog.NetworkActivityLog
	if err := json.Unmarshal(netlogData, &nal); err != nil {
		return nil, fmt.Errorf("decoding netlog: %w", err)
	}
	if len(nal.HTTPRequests) == 0 {
		return nil, nil
	}

	// Build port -> action ID index. RangeActions is parallel, so use sync.Map.
	var portToActionID sync.Map
	err = sgquery.RangeActions(ctx, sg, func(ctx context.Context, a *sgpb.Action) error {
		for digestStr := range a.GetOutputs() {
			dg, err := pbdigest.NewFromString(digestStr)
			if err != nil {
				continue
			}
			r, ok := resources[dg]
			if !ok || r.GetType() != sgpb.ResourceType_RESOURCE_TYPE_NETWORK_ADDRESS {
				continue
			}
			addr := r.GetNetworkAddrInfo().GetAddress()
			sport, err := extractSourcePort(addr)
			if err != nil {
				continue
			}
			portToActionID.Store(sport, a.GetId())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Join netlog entries to action IDs.
	result := map[int64][]string{}
	for _, entry := range nal.HTTPRequests {
		v, ok := portToActionID.Load(entry.PeerPort)
		if !ok {
			continue
		}
		aid := v.(int64)
		url := entry.Scheme + "://" + entry.Host + entry.Path
		result[aid] = append(result[aid], url)
	}
	return result, nil
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

func parseArgs(cfg *Config, args []string) error {
	cfg.SysgraphPath = args[0]
	return nil
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.Int64Var(&cfg.RootID, "root-id", 0, "filter to subtree starting from this action ID (from --ids output)")
	set.Int64Var(&cfg.AncestorID, "ancestor-id", 0, "show the ancestor path from root down to this action ID (from --ids output)")
	set.IntVar(&cfg.MaxDepth, "max-depth", 0, "limit recursion depth (0 = unlimited)")
	set.IntVar(&cfg.Collapse, "collapse", 0, "group N+ consecutive siblings with same executable (0 = disable)")
	set.BoolVar(&cfg.ShowForks, "show-forks", false, "include fork-only actions in output")
	set.BoolVar(&cfg.ShowFiles, "show-files", false, "show file reads and writes for each process")
	set.StringVar(&cfg.NetlogPath, "netlog", "", "path to netlog.json for URL annotations per process")
	set.BoolVar(&cfg.Verbose, "v", false, "verbose output (show ids, cwd, and duration)")
	return set
}
