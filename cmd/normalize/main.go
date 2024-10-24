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
	infile        = flag.String("infile", "", "Input path to the file to be normalized.")
	outfile       = flag.String("outfile", "", "Output path to which the normalized file will be written.")
	enablePasses  = flag.String("enable-passes", "all", "Enable the comma-separated set of normalization passes or 'all'. -help for full list of passes")
	disablePasses = flag.String("disable-passes", "none", "Disable only the comma-separated set of normalization passes or 'none'. -help for full list of passes")
)

func getName(san any) string {
	switch san.(type) {
	case archive.TarArchiveSanitizer:
		return san.(archive.TarArchiveSanitizer).Name
	case archive.TarEntrySanitizer:
		return san.(archive.TarEntrySanitizer).Name
	case archive.ZipArchiveSanitizer:
		return san.(archive.ZipArchiveSanitizer).Name
	case archive.ZipEntrySanitizer:
		return san.(archive.ZipEntrySanitizer).Name
	default:
		log.Fatalf("unknown sanitizer type: %T", san)
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

type SanitizerRegistry struct {
	sanitizers []any
	byName     map[string]any
}

func NewSanitizerRegistry(sans ...any) SanitizerRegistry {
	reg := SanitizerRegistry{sanitizers: sans}
	reg.byName = make(map[string]any)
	for _, san := range reg.sanitizers {
		reg.byName[getName(san)] = san
	}
	return reg
}

func (reg SanitizerRegistry) Get(name string) (any, bool) {
	val, ok := reg.byName[name]
	return val, ok
}

func (reg SanitizerRegistry) GetAll() []any {
	return reg.sanitizers[:]
}

// determinePasses returns the passes specified with the given pass specs.
//
// - Preserves the order specified in enableSpec. Order of "all" is impl-defined.
// - Disable has precedence over enable.
// - Duplicates are retained and respected.
func determinePasses(sanitizers SanitizerRegistry, enableSpec, disableSpec string) ([]any, error) {
	var toRun []any
	enabled := make(map[string]bool)
	if *enablePasses == "all" {
		for _, pass := range sanitizers.GetAll() {
			toRun = append(toRun, pass)
			enabled[getName(pass)] = true
		}
	} else {
		for _, name := range strings.Split(enableSpec, ",") {
			cleanName := strings.TrimSpace(name)
			if san, ok := sanitizers.Get(cleanName); !ok {
				return nil, fmt.Errorf("unknown pass name: %s", cleanName)
			} else {
				toRun = append(toRun, san)
				enabled[cleanName] = true
			}
		}
	}
	if *disablePasses != "none" {
		if *disablePasses == "all" {
			clear(enabled)
		} else {
			for _, name := range strings.Split(disableSpec, ",") {
				cleanName := strings.TrimSpace(name)
				if _, ok := sanitizers.Get(cleanName); !ok {
					return nil, fmt.Errorf("unknown pass name: %s", cleanName)
				}
				if _, ok := enabled[cleanName]; ok {
					delete(enabled, cleanName)
				}
			}
		}
		// Apply deletions from "enabled".
		toRun = slices.DeleteFunc(toRun, func(san any) bool {
			_, ok := enabled[getName(san)]
			return !ok
		})
	}
	return toRun, nil
}

func run() error {
	sanitizers := NewSanitizerRegistry(archive.AllSanitzers...)

	// Update usage to include available passes.
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nAvailable normalization passes (in default order of application):\n")
		for _, san := range archive.AllSanitzers {
			fmt.Fprintf(os.Stderr, "  - %s\n", getName(san))
		}
	}

	flag.Parse()

	if *infile == "" || *outfile == "" {
		flag.Usage()
		return errors.New("both -infile and -outfile are required")
	}
	toRun, err := determinePasses(sanitizers, *enablePasses, *disablePasses)
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

	err = archive.CanonicalizeWithOpts(out, in, filetype(*infile), archive.CanonicalizeOpts{Sanitizers: toRun})
	return errors.Wrap(err, "canonicalizing file")
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
