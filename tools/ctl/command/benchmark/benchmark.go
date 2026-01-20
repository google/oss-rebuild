// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package benchmark

import (
	"context"
	"encoding/json"
	"flag"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type Config struct {
	Op       string
	InputOne string
	InputTwo string
}

func (c Config) Validate() error {
	if c.Op != "add" && c.Op != "subtract" {
		return errors.Errorf("invalid operation: %s. Expected one of 'add' or 'subtract'", c.Op)
	}
	if c.InputOne == "" || c.InputTwo == "" {
		return errors.New("input1, input2 arguments and the output flag are required")
	}
	return nil
}

type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

func InitDeps(context.Context) (*Deps, error) {
	return &Deps{}, nil
}

func parseArgs(cfg *Config, args []string) error {
	if len(args) != 3 {
		return errors.New("expected exactly 3 arguments: <operation> <input1> <input2>")
	}
	cfg.Op = args[0]
	cfg.InputOne = args[1]
	cfg.InputTwo = args[2]
	return nil
}

func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	base, err := benchmark.ReadBenchmark(cfg.InputOne)
	if err != nil {
		return nil, errors.Wrap(err, "reading base benchmark file")
	}
	second, err := benchmark.ReadBenchmark(cfg.InputTwo)
	if err != nil {
		return nil, errors.Wrap(err, "reading second benchmark file")
	}
	var result benchmark.PackageSet
	switch cfg.Op {
	case "add":
		result, err = benchmark.Add(base, second)
	case "subtract":
		result, err = benchmark.Subtract(base, second)
	default:
		return nil, errors.Errorf("unknown operation: %s", cfg.Op)
	}
	if err != nil {
		return nil, errors.Wrap(err, "performing set operation")
	}
	enc := json.NewEncoder(deps.IO.Out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return nil, errors.Wrap(err, "writing to stdout")
	}
	return &act.NoOutput{}, nil
}

func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "benchmark <add|subtract> <input1> <input2>",
		Short: "Perform set operations on benchmark files",
		Args:  cobra.ExactArgs(3),
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

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	return set
}
