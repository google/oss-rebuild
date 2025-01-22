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
	"github.com/pkg/errors"
)

var (
	infile        = flag.String("infile", "", "Input path to the file to be stabilized.")
	outfile       = flag.String("outfile", "", "Output path to which the stabilized file will be written.")
	enablePasses  = flag.String("enable-passes", "all", "Enable the comma-separated set of stabilizers or 'all'. -help for full list of options")
	disablePasses = flag.String("disable-passes", "none", "Disable only the comma-separated set of stabilizers or 'none'. -help for full list of options")
)

func getName(san any) string {
	switch san.(type) {
	case archive.TarArchiveStabilizer:
		return san.(archive.TarArchiveStabilizer).Name
	case archive.TarEntryStabilizer:
		return san.(archive.TarEntryStabilizer).Name
	case archive.ZipArchiveStabilizer:
		return san.(archive.ZipArchiveStabilizer).Name
	case archive.ZipEntryStabilizer:
		return san.(archive.ZipEntryStabilizer).Name
	case archive.GzipStabilizer:
		return san.(archive.GzipStabilizer).Name
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
	case ".tgz", ".crate", ".gz", ".Z":
		return archive.TarGzFormat
	case ".zip", ".whl", ".egg", ".jar":
		return archive.ZipFormat
	default:
		return archive.RawFormat
	}
}

type StabilizerRegistry struct {
	stabilizers []any
	byName      map[string]any
}

func NewStabilizerRegistry(stabs ...any) StabilizerRegistry {
	reg := StabilizerRegistry{stabilizers: stabs}
	reg.byName = make(map[string]any)
	for _, san := range reg.stabilizers {
		reg.byName[getName(san)] = san
	}
	return reg
}

func (reg StabilizerRegistry) Get(name string) (any, bool) {
	val, ok := reg.byName[name]
	return val, ok
}

func (reg StabilizerRegistry) GetAll() []any {
	return reg.stabilizers[:]
}

// determinePasses returns the passes specified with the given pass specs.
//
// - Preserves the order specified in enableSpec. Order of "all" is impl-defined.
// - Disable has precedence over enable.
// - Duplicates are retained and respected.
func determinePasses(reg StabilizerRegistry, enableSpec, disableSpec string) ([]any, error) {
	var toRun []any
	enabled := make(map[string]bool)
	switch enableSpec {
	case "all":
		for _, pass := range reg.GetAll() {
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
	toRun = slices.DeleteFunc(toRun, func(san any) bool {
		_, ok := enabled[getName(san)]
		return !ok
	})
	return toRun, nil
}

func run() error {
	stabilizers := NewStabilizerRegistry(archive.AllStabilizers...)

	// Update usage to include available passes.
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nAvailable stabilizers (in default order of application):\n")
		for _, san := range archive.AllStabilizers {
			fmt.Fprintf(os.Stderr, "  - %s\n", getName(san))
		}
	}

	flag.Parse()

	if *infile == "" || *outfile == "" {
		flag.Usage()
		return errors.New("both -infile and -outfile are required")
	}
	toRun, err := determinePasses(stabilizers, *enablePasses, *disablePasses)
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
	err = archive.StabilizeWithOpts(out, in, filetype(*infile), archive.StabilizeOpts{Stabilizers: toRun})
	return errors.Wrap(err, "stabilizing file")
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
