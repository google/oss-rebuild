// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(v string) error {
	if v == "" {
		*s = nil
		return nil
	}
	*s = strings.Split(v, ",")
	return nil
}

// Config holds all configuration for the stabilization action.
type Config struct {
	Infile        string
	Outfile       string
	EnablePasses  []string
	DisablePasses []string
	Ecosystem     string
}

// Validate checks the input parameters.
func (c Config) Validate() error {
	if c.Infile == "" || c.Outfile == "" {
		return errors.New("both infile and outfile are required")
	}
	return nil
}

// Deps defines the dependencies for the stabilization action.
type Deps struct {
	IO cli.IO
	FS billy.Filesystem
}

// SetIO sets the IO streams.
func (d *Deps) SetIO(io cli.IO) {
	d.IO = io
}

// InitDeps initializes the dependencies.
func InitDeps(ctx context.Context) (*Deps, error) {
	return &Deps{
		FS: osfs.New("/"),
	}, nil
}

// StabilizeFile is the action to stabilize a file.
func StabilizeFile(ctx context.Context, cfg Config, d *Deps) (*act.NoOutput, error) {
	candidates, err := eligiblePasses(cfg.Infile)
	if err != nil {
		return nil, err
	}

	stabilizers := NewStabilizerRegistry(stabilize.AllStabilizers...)
	toRun, err := determinePasses(stabilizers, cfg.EnablePasses, cfg.DisablePasses, candidates)
	if err != nil {
		return nil, err
	}

	in, err := d.FS.Open(cfg.Infile)
	if err != nil {
		return nil, errors.Wrap(err, "opening input file")
	}
	defer in.Close()

	out, err := d.FS.Create(cfg.Outfile)
	if err != nil {
		return nil, errors.Wrap(err, "creating output file")
	}
	defer out.Close()

	var names []string
	for _, stab := range toRun {
		names = append(names, stab.Name)
	}
	fmt.Fprintf(d.IO.Err, "Applying stablizers: {%s}\n", strings.Join(names, ", "))

	err = stabilize.StabilizeWithOpts(out, in, filetype(cfg.Infile), stabilize.StabilizeOpts{Stabilizers: toRun})
	if err != nil {
		return nil, errors.Wrap(err, "stabilizing file")
	}

	return &act.NoOutput{}, nil
}

func filetype(path string) archive.Format {
	ext := filepath.Ext(path)
	switch ext {
	case ".tar", ".gem":
		return archive.TarFormat
	case ".tgz", ".crate":
		return archive.TarGzFormat
	case ".gz", ".Z":
		if filepath.Ext(strings.TrimSuffix(path, ext)) == ".tar" {
			return archive.TarGzFormat
		}
		return archive.UnknownFormat
	case ".zip", ".whl", ".egg", ".jar":
		return archive.ZipFormat
	default:
		return archive.RawFormat
	}
}

// stabilizerRegistry facilitates looking up stabilizers by name.
type stabilizerRegistry struct {
	stabilizers []stabilize.Stabilizer
	byName      map[string]stabilize.Stabilizer
}

// NewStabilizerRegistry creates a registry from the provided stabilizers.
func NewStabilizerRegistry(stabs ...stabilize.Stabilizer) stabilizerRegistry {
	reg := stabilizerRegistry{stabilizers: stabs}
	reg.byName = make(map[string]stabilize.Stabilizer)
	for _, san := range reg.stabilizers {
		reg.byName[san.Name] = san
	}
	return reg
}

// Get returns the stabilizer with the given name.
func (reg stabilizerRegistry) Get(name string) (stabilize.Stabilizer, bool) {
	val, ok := reg.byName[name]
	return val, ok
}

// GetAll returns all stabilizers in the registry.
func (reg stabilizerRegistry) GetAll() []stabilize.Stabilizer {
	return reg.stabilizers[:]
}

// determinePasses returns the passes specified with the given pass specs.
//
// - Preserves the order specified in enableSpec. Order of "all" is impl-defined.
// - Disable has precedence over enable.
// - Duplicates are retained and respected.
func determinePasses(reg stabilizerRegistry, enableSpec, disableSpec []string, eligible []stabilize.Stabilizer) ([]stabilize.Stabilizer, error) {
	var toRun []stabilize.Stabilizer
	enabled := make(map[string]bool)
	switch {
	case slices.Equal(enableSpec, []string{"all"}):
		for _, pass := range eligible {
			toRun = append(toRun, pass)
			enabled[pass.Name] = true
		}
	case slices.Equal(enableSpec, []string{"none"}):
		// No passes enabled.
	default:
		for _, name := range enableSpec {
			cleanName := strings.TrimSpace(name)
			if san, ok := reg.Get(cleanName); !ok {
				return nil, errors.Errorf("unknown pass name: %s", cleanName)
			} else if !slices.ContainsFunc(eligible, func(e stabilize.Stabilizer) bool { return e.Name == san.Name }) {
				return nil, errors.Errorf("ineligible pass for artifact: %s", cleanName)
			} else {
				toRun = append(toRun, san)
				enabled[cleanName] = true
			}
		}
	}
	switch {
	case slices.Equal(disableSpec, []string{"all"}):
		clear(enabled)
	case slices.Equal(disableSpec, []string{"none"}):
		// No passes disabled.
	default:
		for _, name := range disableSpec {
			cleanName := strings.TrimSpace(name)
			if _, ok := reg.Get(cleanName); !ok {
				return nil, fmt.Errorf("unknown pass name: %s", cleanName)
			}
			if _, ok := enabled[cleanName]; ok {
				delete(enabled, cleanName)
			}
		}
	}
	// Apply deletions from "enabled" map.
	toRun = slices.DeleteFunc(toRun, func(san stabilize.Stabilizer) bool {
		_, ok := enabled[san.Name]
		return !ok
	})
	return toRun, nil
}

func candidateEcosystems(filename string) []rebuild.Ecosystem {
	ext := filepath.Ext(filename)
	switch ext {
	case ".jar":
		return []rebuild.Ecosystem{rebuild.Maven}
	case ".pom":
		return []rebuild.Ecosystem{rebuild.Maven}
	case ".whl", ".egg":
		return []rebuild.Ecosystem{rebuild.PyPI}
	case ".crate":
		return []rebuild.Ecosystem{rebuild.CratesIO}
	case ".tgz":
		return []rebuild.Ecosystem{rebuild.NPM, rebuild.PyPI}
	case ".gz":
		if strings.HasSuffix(filename, ".tar.gz") {
			return []rebuild.Ecosystem{rebuild.NPM, rebuild.PyPI}
		} else {
			return []rebuild.Ecosystem{rebuild.PyPI}
		}
	case ".tar":
		return []rebuild.Ecosystem{rebuild.PyPI}
	case ".gem":
		return []rebuild.Ecosystem{rebuild.RubyGems}
	case ".zip":
		return []rebuild.Ecosystem{rebuild.PyPI}
	default:
		return nil
	}
}

var ErrAmbiguousEcosystem = errors.New("ambiguous ecosystem detection for file")

func eligiblePasses(filename string) ([]stabilize.Stabilizer, error) {
	candidates := candidateEcosystems(filename)
	if len(candidates) == 0 {
		return nil, errors.New("no eligible ecosystems for file")
	}
	var result []stabilize.Stabilizer
	for i, e := range candidates {
		stabs, err := stability.StabilizersForTarget(rebuild.Target{Ecosystem: e, Artifact: filename})
		if err != nil {
			return nil, errors.Wrapf(err, "getting stabilizers for %s candidate ecosystem", e)
		}
		if i == 0 {
			result = stabs
		} else if !slices.EqualFunc(result, stabs, func(s1, s2 stabilize.Stabilizer) bool {
			return s1.Name == s2.Name
		}) {
			return nil, errors.Wrapf(ErrAmbiguousEcosystem, "ecosystem %s suggests different stabilizers than %s", candidates[0], e)
		}
	}
	return result, nil
}

// Command creates a new stabilize command instance.
func Command() *cobra.Command {
	cfg := Config{}

	// Generate help text
	var stabs []string
	for _, san := range stabilize.AllStabilizers {
		stabs = append(stabs, san.Name)
	}
	helpText := fmt.Sprintf("Available stabilizers (in default order of application):\n  - %s", strings.Join(stabs, "\n  - "))

	cmd := &cobra.Command{
		Use:   "stabilize",
		Short: "Stabilize build artifacts",
		Long:  "Stabilize build artifacts.\n\n" + helpText,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			StabilizeFile,
		),
	}
	cmd.Flags().AddGoFlagSet(FlagSet(cmd.Name(), &cfg))
	return cmd
}

// FlagSet returns the command-line flags for the Config struct.
func FlagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Infile, "infile", "", "Input path to the file to be stabilized.")
	set.StringVar(&cfg.Outfile, "outfile", "", "Output path to which the stabilized file will be written.")
	set.Var((*stringSlice)(&cfg.EnablePasses), "enable-passes", "Enable the comma-separated set of stabilizers or 'all'.")
	cfg.EnablePasses = []string{"all"} // default
	set.Var((*stringSlice)(&cfg.DisablePasses), "disable-passes", "Disable only the comma-separated set of stabilizers or 'none'.")
	cfg.DisablePasses = []string{"none"} // default
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "The package ecosystem of the artifact. Required when ambiguous from the file extension.")
	return set
}
