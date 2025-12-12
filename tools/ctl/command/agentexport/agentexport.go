// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentexport

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the get-sessions command.
type Config struct {
	Project         string
	DefDir          string
	Since           string
	Until           string
	Ecosystem       string
	Package         string
	Dryrun          bool
	IncludeExisting bool
	Yes             bool
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
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

// importAgentResult holds information about an import operation
type importAgentResult struct {
	sessionID string
	target    rebuild.Target
	imported  bool
	skipped   bool
	existed   bool
	err       error
}

// writeAgentDefinition writes a build definition to the asset store
func writeAgentDefinition(ctx context.Context, buildDefs *rebuild.FilesystemAssetStore, target rebuild.Target, def schema.BuildDefinition) error {
	buildDefAsset := rebuild.BuildDef.For(target)
	w, err := buildDefs.Writer(ctx, buildDefAsset)
	if err != nil {
		return errors.Wrap(err, "opening build definition for writing")
	}
	defer w.Close()
	enc := yaml.NewEncoder(w)
	if err := enc.Encode(&def); err != nil {
		return errors.Wrap(err, "encoding build definition")
	}
	return nil
}

// Handler contains the business logic for getting sessions.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	dex, err := rundex.NewFirestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to rundex")
	}
	// Parse time filters
	req := rundex.FetchSessionsReq{
		PartialTarget: &rebuild.Target{
			Ecosystem: rebuild.Ecosystem(cfg.Ecosystem),
			Package:   cfg.Package,
		},
		StopReason: schema.AgentCompleteReasonSuccess,
	}
	if cfg.Since != "" {
		// Try parsing as duration first
		if dur, err := time.ParseDuration(cfg.Since); err == nil {
			req.Since = time.Now().Add(-dur)
		} else if t, err := time.Parse(time.RFC3339, cfg.Since); err == nil {
			req.Since = t
		} else {
			return nil, errors.Errorf("invalid --since value: %s (expected duration like '24h' or RFC3339 timestamp)", cfg.Since)
		}
	}
	if cfg.Until != "" {
		if t, err := time.Parse(time.RFC3339, cfg.Until); err == nil {
			req.Until = t
		} else {
			return nil, errors.Errorf("invalid --until value: %s (expected RFC3339 timestamp)", cfg.Until)
		}
	}
	// Query successful agent sessions
	sessions, err := dex.FetchSessions(ctx, &req)
	if err != nil {
		return nil, errors.Wrap(err, "querying agent sessions")
	}
	if len(sessions) == 0 {
		fmt.Fprintln(deps.IO.Out, "No successful agent sessions found matching the filters.")
		return nil, nil
	}
	fmt.Fprintf(deps.IO.Out, "Found %d successful agent session(s)\n\n", len(sessions))
	// Create asset store for definitions
	var buildDefs *rebuild.FilesystemAssetStore
	if cfg.DefDir != "" {
		buildDefs = rebuild.NewFilesystemAssetStore(osfs.New(cfg.DefDir))
	} else {
		var err error
		if buildDefs, err = localfiles.BuildDefs(); err != nil {
			return nil, errors.Wrap(err, "failed to create local build def asset store")
		}
	}
	// Process each session
	var results []importAgentResult
	for _, session := range sessions {
		result := importAgentResult{
			sessionID: session.ID,
			target:    session.Target,
		}
		iterations, err := dex.FetchIterations(ctx, &rundex.FetchIterationsReq{SessionID: session.ID, IterationIDs: []string{session.SuccessIteration}})
		if err == nil && len(iterations) == 0 {
			err = errors.New("no success iteration found")
		}
		if err == nil && len(iterations) == 1 && iterations[0].Strategy == nil {
			err = errors.New("no strategy found")
		}
		if err != nil {
			result.err = err
			results = append(results, result)
			continue
		}
		strategy := iterations[0].Strategy
		// Check if definition already exists
		buildDefAsset := rebuild.BuildDef.For(session.Target)
		var existingDef *schema.BuildDefinition
		if r, err := buildDefs.Reader(ctx, buildDefAsset); err == nil {
			var def schema.BuildDefinition
			if yaml.NewDecoder(r).Decode(&def) == nil {
				existingDef = &def
			}
			r.Close()
		}
		// Skip if not --include-existing and definition exists
		if !cfg.IncludeExisting && existingDef != nil {
			result.skipped = true
			result.existed = true
			results = append(results, result)
			continue
		}
		// Display strategy for review
		fmt.Fprintf(deps.IO.Out, "--- Session: %s ---\n", session.ID)
		fmt.Fprintf(deps.IO.Out, "Target: %s %s@%s (%s)\n", session.Target.Ecosystem, session.Target.Package, session.Target.Version, session.Target.Artifact)
		fmt.Fprintf(deps.IO.Out, "Created: %s\n\n", session.Created.Format(time.RFC3339))
		// Show the strategy YAML
		strategyYAML, err := yaml.Marshal(strategy)
		if err != nil {
			result.err = errors.Wrap(err, "marshalling strategy")
			results = append(results, result)
			continue
		}
		fmt.Fprintf(deps.IO.Out, "Strategy:\n%s\n", string(strategyYAML))
		// Show diff if existing definition
		// TODO: Use diffr library with bytes diffing
		if existingDef != nil {
			existingYAML, _ := yaml.Marshal(existingDef)
			fmt.Fprintf(deps.IO.Out, "Existing definition:\n%s\n", string(existingYAML))
		}
		// Prompt for import (unless --yes or --dryrun)
		if cfg.Dryrun {
			fmt.Fprintln(deps.IO.Out, "[dry-run] Skipping import")
			result.imported = true
			results = append(results, result)
			continue
		}
		shouldImport := cfg.Yes
		if !shouldImport {
			fmt.Fprint(deps.IO.Out, "Import this definition? [y]es / [n]o / [q]uit: ")
			var response string
			fmt.Scanln(&response)
			response = strings.ToLower(strings.TrimSpace(response))
			switch response {
			case "y", "yes":
				shouldImport = true
			case "q", "quit":
				fmt.Fprintln(deps.IO.Out, "Quitting...")
				break
			default:
				result.skipped = true
				results = append(results, result)
				fmt.Fprintln(deps.IO.Out, "")
				continue
			}
		}
		if shouldImport {
			// Write the definition
			def := schema.BuildDefinition{StrategyOneOf: strategy}
			if err := writeAgentDefinition(ctx, buildDefs, session.Target, def); err != nil {
				result.err = errors.Wrap(err, "writing definition")
				results = append(results, result)
				continue
			}
			result.imported = true
			fmt.Fprintln(deps.IO.Out, "Imported successfully")
		}
		results = append(results, result)
	}
	// Print summary
	var imported, skipped, errored int
	for _, r := range results {
		if r.imported {
			imported++
		} else if r.skipped {
			skipped++
		} else if r.err != nil {
			errored++
			fmt.Fprintf(deps.IO.Err, "Error for %s/%s@%s: %v\n", r.target.Ecosystem, r.target.Package, r.target.Version, r.err)
		}
	}
	fmt.Fprintf(deps.IO.Out, "\nSummary: %d imported, %d skipped, %d errors\n", imported, skipped, errored)
	return nil, nil
}

// Command creates a new import-agent-definitions command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "import-agent-definitions --project <project> [--def-dir <path>] [--since <time>] [--until <time>] [--ecosystem <eco>] [--package <pkg>] [--dryrun] [--exclude-existing] [--yes]",
		Short: "Import agent-generated build definitions into the definitions repo",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs, InitDeps, Handler),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the project from which to agent results")
	set.StringVar(&cfg.DefDir, "def-dir", "", "the directory where build definitions are stored")
	set.StringVar(&cfg.Since, "since", "", "starting bound of session import, expected as a duration like '24h' or RFC3339 timestamp")
	set.StringVar(&cfg.Until, "until", "", "ending bound of session import, expected as a RFC3339 timestamp")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "filter the ecosystem to import")
	set.StringVar(&cfg.Package, "package", "", "filter the package to import")
	set.BoolVar(&cfg.Dryrun, "dryrun", false, "execute as dryrun mode")
	set.BoolVar(&cfg.IncludeExisting, "include-existing", false, "include packages that already have a build definition in the import")
	set.BoolVar(&cfg.Yes, "yes", false, "auto-approve all import actions")
	return set
}
