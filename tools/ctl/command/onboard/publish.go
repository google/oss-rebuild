// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/buildinfo"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type publishConfig struct {
	Prevalence string
	Dest       string
}

func (c publishConfig) Validate() error {
	if c.Prevalence == "" {
		return errors.New("prevalence is required")
	}
	if c.Dest == "" {
		return errors.New("dest is required")
	}
	return nil
}

// publishHandler assembles the signal database from the ranked exports and
// uploads it to the destination as one object write, so consumers never
// observe a partial publish.
func publishHandler(ctx context.Context, cfg publishConfig, deps *Deps) (*act.NoOutput, error) {
	resolve := billyx.NewResolver()
	prevs, err := readExport[signals.PrevalenceRecord](ctx, resolve, cfg.Prevalence)
	if err != nil {
		return nil, errors.Wrap(err, "reading prevalence export")
	}
	dir, err := os.MkdirTemp("", "signals-publish-")
	if err != nil {
		return nil, errors.Wrap(err, "creating build directory")
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "signals.db")
	counts, err := signals.Build(path, prevs, signals.Meta{BuiltAt: time.Now().UTC(), ToolVersion: buildinfo.Version})
	if err != nil {
		return nil, errors.Wrap(err, "building signal database")
	}
	dest, err := resolve.DirFS(ctx, cfg.Dest)
	if err != nil {
		return nil, err
	}
	if err := sqlitex.Publish(dest, signals.Object, path); err != nil {
		return nil, errors.Wrap(err, "uploading signal database")
	}
	fmt.Fprintf(deps.IO.Out, "wrote %s (%d packages, %d versions)\n",
		signals.Object, counts[signals.TablePackageSignals], counts[signals.TableVersionSignals])
	return &act.NoOutput{}, nil
}

// readExport resolves, opens, and fully decodes one JSONL export: the
// database build needs every row in hand.
func readExport[T any](ctx context.Context, resolve *billyx.Resolver, uri string) ([]T, error) {
	fsys, name, err := resolve.FS(ctx, uri)
	if err != nil {
		return nil, errors.Wrap(err, "resolving export")
	}
	r, err := fsys.Open(name)
	if err != nil {
		return nil, errors.Wrap(err, "opening export")
	}
	defer r.Close()
	return jsonl.DecodeAll[T](r)
}

// publishCommand creates the `onboard priority publish` subcommand.
func publishCommand() *cobra.Command {
	cfg := publishConfig{}
	cmd := &cobra.Command{
		Use:   "publish --prevalence <uri> --dest <gs://...|file://...>",
		Short: "Assemble and publish the signal database from the ranked exports",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[publishConfig], InitDeps, publishHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Prevalence, "prevalence", "", "prevalence JSONL export (gs:// URI or local path)")
	set.StringVar(&cfg.Dest, "dest", "", "destination URI the signal database publishes under")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
