// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/pkg/errors"
)

var (
	infile        = flag.String("infile", "", "Input path to the file to be stabilized.")
	outfile       = flag.String("outfile", "", "Output path to which the stabilized file will be written.")
	enablePasses  = flag.String("enable-passes", "all", "Enable the comma-separated set of stabilizers or 'all'. -help for full list of options")
	disablePasses = flag.String("disable-passes", "none", "Disable only the comma-separated set of stabilizers or 'none'. -help for full list of options")
	ecosystem     = flag.String("ecosystem", "", "The package ecosystem of the artifact. Required when ambiguous from the file extension.")
)

func getName(san stabilize.Stabilizer) string {
	switch san.(type) {
	case stabilize.TarArchiveStabilizer:
		return san.(stabilize.TarArchiveStabilizer).Name
	case stabilize.TarEntryStabilizer:
		return san.(stabilize.TarEntryStabilizer).Name
	case stabilize.ZipArchiveStabilizer:
		return san.(stabilize.ZipArchiveStabilizer).Name
	case stabilize.ZipEntryStabilizer:
		return san.(stabilize.ZipEntryStabilizer).Name
	case stabilize.GzipStabilizer:
		return san.(stabilize.GzipStabilizer).Name
	default:
		log.Fatalf("unknown stabilizer type: %T", san)
		return "" // unreachable
	}
}

func filetype(path string) archive.Format {
	ext := filepath.Ext(path)
	switch ext {
	case ".tar":
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

type StabilizerRegistry struct {
	stabilizers []stabilize.Stabilizer
	byName      map[string]stabilize.Stabilizer
}

func NewStabilizerRegistry(stabs ...stabilize.Stabilizer) StabilizerRegistry {
	reg := StabilizerRegistry{stabilizers: stabs}
	reg.byName = make(map[string]stabilize.Stabilizer)
	for _, san := range reg.stabilizers {
		reg.byName[getName(san)] = san
	}
	return reg
}

func (reg StabilizerRegistry) Get(name string) (stabilize.Stabilizer, bool) {
	val, ok := reg.byName[name]
	return val, ok
}

func (reg StabilizerRegistry) GetAll() []stabilize.Stabilizer {
	return reg.stabilizers[:]
}

// determinePasses returns the passes specified with the given pass specs.
//
// - Preserves the order specified in enableSpec. Order of "all" is impl-defined.
// - Disable has precedence over enable.
// - Duplicates are retained and respected.
func determinePasses(reg StabilizerRegistry, enableSpec, disableSpec string, eligible []stabilize.Stabilizer) ([]stabilize.Stabilizer, error) {
	var toRun []stabilize.Stabilizer
	enabled := make(map[string]bool)
	switch enableSpec {
	case "all":
		for _, pass := range eligible {
			toRun = append(toRun, pass)
			enabled[getName(pass)] = true
		}
	case "", "none":
		// No passes enabled.
	default:
		for _, name := range strings.Split(enableSpec, ",") {
			cleanName := strings.TrimSpace(name)
			if san, ok := reg.Get(cleanName); !ok {
				return nil, errors.Errorf("unknown pass name: %s", cleanName)
			} else if !slices.Contains(eligible, san) {
				return nil, errors.Errorf("ineligible pass for artifact: %s", cleanName)
			} else {
				toRun = append(toRun, san)
				enabled[cleanName] = true
			}
		}
	}
	switch disableSpec {
	case "all":
		clear(enabled)
	case "", "none":
		// No passes disabled.
	default:
		for _, name := range strings.Split(disableSpec, ",") {
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
		_, ok := enabled[getName(san)]
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
			return getName(s1) == getName(s2)
		}) {
			return nil, errors.Wrapf(ErrAmbiguousEcosystem, "ecosystem %s suggests different stabilizers than %s", candidates[0], e)
		}
	}
	return result, nil
}

func run() error {
	stabilizers := NewStabilizerRegistry(stabilize.AllStabilizers...)

	// Update usage to include available passes.
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nAvailable stabilizers (in default order of application):\n")
		for _, san := range stabilize.AllStabilizers {
			fmt.Fprintf(os.Stderr, "  - %s\n", getName(san))
		}
	}

	flag.Parse()

	if *infile == "" || *outfile == "" {
		flag.Usage()
		return errors.New("both -infile and -outfile are required")
	}

	candidates, err := eligiblePasses(*infile)
	if err != nil {
		flag.Usage()
		return err
	}

	toRun, err := determinePasses(stabilizers, *enablePasses, *disablePasses, candidates)
	if err != nil {
		flag.Usage()
		return err
	}

	in, err := os.Open(*infile)
	if err != nil {
		return errors.Wrap(err, "opening input file")
	}
	defer in.Close()

	out, err := os.Create(*outfile)
	if err != nil {
		return errors.Wrap(err, "creating output file")
	}
	defer out.Close()

	var names []string
	for _, stab := range toRun {
		names = append(names, getName(stab))
	}
	log.Printf("Applying stablizers: {%s}", strings.Join(names, ", "))
	err = stabilize.StabilizeWithOpts(out, in, filetype(*infile), stabilize.StabilizeOpts{Stabilizers: toRun})
	return errors.Wrap(err, "stabilizing file")
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
