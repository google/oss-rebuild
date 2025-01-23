package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	// NOTE: This is regrettable but the expedient way of gathering the implicit
	// registrations while they're opaque. This should be refactored away.
	_ "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	_ "github.com/google/oss-rebuild/pkg/rebuild/debian"
	_ "github.com/google/oss-rebuild/pkg/rebuild/maven"
	_ "github.com/google/oss-rebuild/pkg/rebuild/npm"
	_ "github.com/google/oss-rebuild/pkg/rebuild/pypi"
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type config struct {
	gitLocation    string // in form "repo@ref"
	gitLocationDir string
	artifactName   string
	buildEnv       rebuild.BuildEnv
	toolPaths      stringSlice
}

func determineTarget(artifactName string) (*rebuild.Target, error) {
	// Handle PyPI
	if strings.HasSuffix(artifactName, ".whl") ||
		strings.HasSuffix(artifactName, ".tar.gz") ||
		strings.HasSuffix(artifactName, ".zip") {
		// Expected format: package-version[-extras].whl
		// or: package-version.tar.gz
		parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
			artifactName, ".whl"), ".tar.gz"), ".zip"), "-")
		if len(parts) < 2 {
			return nil, errors.Errorf("invalid PyPI artifact name format: %s", artifactName)
		}
		return &rebuild.Target{
			Ecosystem: rebuild.PyPI,
			Package:   parts[0],
			Version:   parts[1],
			Artifact:  artifactName,
		}, nil
	}

	// Handle NPM
	if strings.HasSuffix(artifactName, ".tgz") {
		// Expected format: package-version.tgz
		base := strings.TrimSuffix(artifactName, ".tgz")
		parts := strings.Split(base, "-")
		if len(parts) < 2 {
			return nil, errors.Errorf("invalid NPM artifact name format: %s", artifactName)
		}
		return &rebuild.Target{
			Ecosystem: rebuild.NPM,
			Package:   parts[0],
			Version:   parts[1],
			Artifact:  artifactName,
		}, nil
	}

	// Handle Maven
	if strings.HasSuffix(artifactName, ".jar") || strings.HasSuffix(artifactName, ".pom") {
		// Expected format: artifact-version[-classifier].jar
		// or: artifact-version.pom
		base := strings.TrimSuffix(strings.TrimSuffix(artifactName, ".jar"), ".pom")
		parts := strings.Split(base, "-")
		if len(parts) < 2 {
			return nil, errors.Errorf("invalid Maven artifact name format: %s", artifactName)
		}
		return &rebuild.Target{
			Ecosystem: rebuild.Maven,
			Package:   parts[0],
			Version:   parts[1],
			Artifact:  artifactName,
		}, nil
	}

	// Handle Crates.io
	if strings.HasSuffix(artifactName, ".crate") {
		// Expected format: package-version.crate
		base := strings.TrimSuffix(artifactName, ".crate")
		parts := strings.Split(base, "-")
		if len(parts) < 2 {
			return nil, errors.Errorf("invalid Crates.io artifact name format: %s", artifactName)
		}
		return &rebuild.Target{
			Ecosystem: rebuild.CratesIO,
			Package:   parts[0],
			Version:   parts[1],
			Artifact:  artifactName,
		}, nil
	}

	// Handle Debian
	if strings.HasSuffix(artifactName, ".deb") {
		// Expected format: package_version_arch.deb
		base := strings.TrimSuffix(artifactName, ".deb")
		parts := strings.Split(base, "_")
		if len(parts) < 2 {
			return nil, errors.Errorf("invalid Debian package name format: %s", artifactName)
		}
		return &rebuild.Target{
			Ecosystem: rebuild.Debian,
			Package:   parts[0],
			Version:   parts[1],
			Artifact:  artifactName,
		}, nil
	}

	return nil, errors.Errorf("unable to determine ecosystem from artifact name: %s", artifactName)
}

func resolveGlobPatterns(fs billy.Filesystem, patterns []string) ([]string, error) {
	var allMatches []string
	for _, pattern := range patterns {
		matches, err := util.Glob(fs, pattern)
		if err != nil {
			return nil, errors.Wrapf(err, "walking directory for pattern %q", pattern)
		}
		allMatches = append(allMatches, matches...)
	}
	return allMatches, nil
}

func loadTools(fs billy.Filesystem, paths []string) error {
	matches, err := resolveGlobPatterns(fs, paths)
	if err != nil {
		return errors.Wrap(err, "resolving tool paths")
	}
	for _, path := range matches {
		f, err := fs.Open(path)
		if err != nil {
			return errors.Wrapf(err, "opening tool file %q", path)
		}
		defer f.Close()
		var tool flow.Tool
		ext := strings.ToLower(filepath.Ext(path))
		if err := yaml.NewDecoder(f).Decode(&tool); err != nil {
			if !slices.Contains([]string{".yaml", ".yml", ".json"}, ext) {
				return errors.Wrapf(err, "decoding YAML tool with unexpected extension %q", path)
			} else {
				return errors.Wrapf(err, "decoding YAML tool %q", path)
			}
		}
		if err := flow.Tools.Register(&tool); err != nil {
			return errors.Wrapf(err, "registering tool from %q", path)
		}
	}
	return nil
}
func parseFlags() (*config, error) {
	cfg := &config{}

	// Location flags
	flag.StringVar(&cfg.gitLocation, "git-location", "", "Source repository and reference (format: repo@ref)")
	flag.StringVar(&cfg.gitLocationDir, "git-location-dir", ".", "Source directory within repository")

	// Target flags
	flag.StringVar(&cfg.artifactName, "artifact", "", "Target artifact name (used to auto-detect ecosystem, package, and version)")

	// Build environment flags
	flag.StringVar(&cfg.buildEnv.TimewarpHost, "timewarp-host", "", "Timewarp host address")
	flag.BoolVar(&cfg.buildEnv.PreferPreciseToolchain, "precise-toolchain", false, "Prefer precise toolchain")
	flag.BoolVar(&cfg.buildEnv.HasRepo, "has-repo", false, "Whether the build environment has repository access")

	// Tool paths
	flag.Var(&cfg.toolPaths, "tools", "Path to tool definition files (supports yaml, yml, json)")

	flag.Parse()

	// Validate git location
	if cfg.gitLocation == "" {
		return nil, errors.New("--git-location is required (format: repo@ref)")
	}
	parts := strings.SplitN(cfg.gitLocation, "@", 2)
	if len(parts) != 2 {
		return nil, errors.Errorf("invalid git location format (expected repo@ref): %s", cfg.gitLocation)
	}

	// Validate artifact name
	if cfg.artifactName == "" {
		return nil, errors.New("--artifact is required")
	}

	return cfg, nil
}

type runner struct {
	fs     billy.Filesystem
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func newRunner() *runner {
	return &runner{
		fs:     osfs.New("/"),
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func (r *runner) run() error {
	cfg, err := parseFlags()
	if err != nil {
		return errors.Wrap(err, "parsing flags")
	}

	if err := loadTools(r.fs, cfg.toolPaths); err != nil {
		return errors.Wrap(err, "loading tools")
	}

	// Determine target from artifact name
	target, err := determineTarget(cfg.artifactName)
	if err != nil {
		return errors.Wrap(err, "determining target from artifact name")
	}

	// Parse git location
	parts := strings.SplitN(cfg.gitLocation, "@", 2)
	location := &rebuild.Location{
		Repo: parts[0],
		Ref:  parts[1],
		Dir:  cfg.gitLocationDir,
	}

	// Read flow definition
	var flowData []byte
	if flag.NArg() == 0 || flag.Arg(0) == "-" {
		flowData, err = io.ReadAll(r.stdin)
		if err != nil {
			return errors.Wrap(err, "reading from stdin")
		}
	} else {
		f, err := r.fs.Open(flag.Arg(0))
		if err != nil {
			return errors.Wrap(err, "opening flow file")
		}
		defer f.Close()
		flowData, err = io.ReadAll(f)
		if err != nil {
			return errors.Wrap(err, "reading flow file")
		}
	}

	// Parse flow definition
	var flowDef struct {
		Steps []flow.Step `json:"steps" yaml:"steps"`
	}
	if err := yaml.Unmarshal(flowData, &flowDef); err != nil {
		return errors.Wrap(err, "parsing flow definition")
	}

	// Resolve steps
	data := flow.Data{
		"Location": location,
		"Target":   target,
		"BuildEnv": cfg.buildEnv,
	}
	fragment, err := flow.ResolveSteps(flowDef.Steps, nil, data)
	if err != nil {
		return errors.Wrap(err, "resolving flow")
	}

	// Output the requirements and script
	if _, err := io.WriteString(r.stdout, `# Requires: `+strings.Join(fragment.Needs, ", ")+"\n"); err != nil {
		return errors.Wrap(err, "writing script to stdout")
	}
	if _, err := io.WriteString(r.stdout, fragment.Script+"\n"); err != nil {
		return errors.Wrap(err, "writing script to stdout")
	}

	return nil
}

func main() {
	r := newRunner()
	if err := r.run(); err != nil {
		io.WriteString(r.stderr, "Error: "+err.Error()+"\n")
		os.Exit(1)
	}
}
