// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"maps"
	"path"
	re "regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/google/shlex"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const workflowsDir = ".github/workflows"

var (
	// cibuildwheelActionPat matches a step using the cibuildwheel action by a
	// release tag (v2.16.2), floating minor tag (v2.16), or commit hash.
	cibuildwheelActionPat = re.MustCompile(`^pypa/cibuildwheel@v?(\d+\.\d+(?:\.\d+)?|[0-9a-f]{40})$`)
	// cibuildwheelReqPat matches an exact cibuildwheel requirement in a command,
	// such as "pipx run cibuildwheel==2.16.2".
	cibuildwheelReqPat = re.MustCompile(`\bcibuildwheel(?:\[[^\]]*\])?\s*==\s*(\d+\.\d+\.\d+)\b`)
)

// ghaWorkflow holds the parts of a GitHub Actions workflow that can pin the
// cibuildwheel version.
// https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
type ghaWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// extractCibuildwheelSpecs returns the cibuildwheel version specs (exact
// versions, minor series, or commit hashes) found in the GitHub Actions
// workflows in tree.
func extractCibuildwheelSpecs(tree *object.Tree) ([]string, error) {
	workflows, err := tree.Tree(workflowsDir)
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "finding %s", workflowsDir)
	}
	var specs []string
	for _, entry := range workflows.Entries {
		if ext := path.Ext(entry.Name); !entry.Mode.IsFile() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		f, err := workflows.TreeEntryFile(&entry)
		if err != nil {
			return nil, errors.Wrapf(err, "finding workflow %s", entry.Name)
		}
		contents, err := f.Contents()
		if err != nil {
			return nil, errors.Wrapf(err, "reading workflow %s", entry.Name)
		}
		specs = append(specs, cibuildwheelSpecs([]byte(contents))...)
	}
	return specs, nil
}

func cibuildwheelSpecs(workflow []byte) []string {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		return nil
	}
	var specs []string
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		for _, step := range wf.Jobs[name].Steps {
			if m := cibuildwheelActionPat.FindStringSubmatch(strings.TrimSpace(step.Uses)); m != nil {
				specs = append(specs, m[1])
			}
			for _, m := range cibuildwheelReqPat.FindAllStringSubmatch(step.Run, -1) {
				specs = append(specs, m[1])
			}
		}
	}
	return specs
}

// extractCibuildwheelDependencies extracts system dependencies from
// [tool.cibuildwheel] hooks and overrides in pyproject.toml under searchDir.
func extractCibuildwheelDependencies(tree *object.Tree, searchDir string, libc platform.LibcImpl) ([]string, error) {
	pyprojPath := path.Clean(path.Join(searchDir, "pyproject.toml"))
	f, err := tree.File(pyprojPath)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "finding %s", pyprojPath)
	}
	pyProject, err := parsing.ReadPyProject(f)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", pyprojPath)
	}
	cfg := pyProject.Tool.Cibuildwheel
	// NOTE: In cibuildwheel, matching overrides replace base and linux hooks by
	// default unless inherit is configured. Taking the union across all matching
	// sections is a conservative over-approximation.
	scripts := slices.Concat(
		tomlStrings(cfg.BeforeAll),
		tomlStrings(cfg.BeforeBuild),
		tomlStrings(cfg.Linux.BeforeAll),
		tomlStrings(cfg.Linux.BeforeBuild),
	)
	for _, ov := range cfg.Overrides {
		if overrideAppliesToTarget(ov.Select, libc) {
			scripts = append(scripts, tomlStrings(ov.BeforeAll)...)
			scripts = append(scripts, tomlStrings(ov.BeforeBuild)...)
		}
	}
	var deps []string
	for _, script := range scripts {
		deps = appendUnique(deps, parsePackageManagerCommands(script, libc)...)
	}
	return deps, nil
}

func tomlStrings(v any) (out []string) {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func overrideAppliesToTarget(selectVal any, libc platform.LibcImpl) bool {
	selectors := tomlStrings(selectVal)
	return len(selectors) == 0 || slices.ContainsFunc(selectors, func(sel string) bool {
		return slices.ContainsFunc(strings.Fields(strings.ToLower(sel)), func(pat string) bool {
			return selectorPatternApplies(pat, libc)
		})
	})
}

var (
	nonLinuxTokens = []string{"macos", "win", "ios", "android", "pyodide"}
	nonX8664Tokens = []string{"i686", "aarch64", "arm64", "armv7l", "ppc64le", "s390x", "riscv64"}
)

// selectorPatternApplies checks whether a cibuildwheel select glob (such as
// "*-manylinux*" or "cp310-manylinux_x86_64") can match an x86_64 Linux target
// with the given libc, using substring checks without consulting the Python tag.
func selectorPatternApplies(pat string, libc platform.LibcImpl) bool {
	contains := func(sub string) bool { return strings.Contains(pat, sub) }
	if slices.ContainsFunc(nonLinuxTokens, contains) {
		return false
	}
	if !contains("x86_64") && !contains("amd64") && slices.ContainsFunc(nonX8664Tokens, contains) {
		return false
	}
	switch libc {
	case platform.Glibc:
		return !contains("musllinux") || contains("manylinux")
	case platform.Musl:
		return !contains("manylinux") || contains("musllinux")
	default:
		return true
	}
}

type pkgManager struct {
	libc       platform.LibcImpl
	subcmd     string
	valueFlags []string
}

var (
	lineContinuationPat = re.MustCompile(`\\\r?\n`)
	shellCommentPat     = re.MustCompile(`(?m)(?:^|[\s;])#.*$`)
	shellCommandSepPat  = re.MustCompile(`[\r\n;&|()]+`)
	shellRedirectPat    = re.MustCompile(`^\d*[<>]+`)
	rpmPkgManager       = pkgManager{
		libc:   platform.Glibc,
		subcmd: "install",
		valueFlags: []string{
			"-c", "--config", "-e", "--errorlevel", "-d", "--debuglevel", "--installroot",
			"--enablerepo", "--disablerepo", "--repo", "--repoid", "--exclude", "-x",
			"--disableexcludes", "--setopt", "--releasever", "--forcearch",
			"--repofrompath", "--enableplugin", "--disableplugin",
		},
	}
	pkgManagers = map[string]pkgManager{
		"yum":      rpmPkgManager,
		"dnf":      rpmPkgManager,
		"microdnf": rpmPkgManager,
		"apk": {
			libc:   platform.Musl,
			subcmd: "add",
			valueFlags: []string{
				"-t", "--virtual", "-p", "--root", "-X", "--repository", "--keys-dir",
				"--repositories-file", "--arch", "--cache-dir", "--wait",
			},
		},
	}
)

// parsePackageManagerCommands extracts system package names from a shell script
// for the target libc (yum/dnf/microdnf install on glibc, apk add on musl).
func parsePackageManagerCommands(script string, libc platform.LibcImpl) []string {
	cleaned := shellCommentPat.ReplaceAllString(lineContinuationPat.ReplaceAllString(script, " "), "")
	var deps []string
	for _, segment := range shellCommandSepPat.Split(cleaned, -1) {
		tokens, err := shlex.Split(segment)
		if err != nil {
			tokens = strings.Fields(segment)
		}
		pmIdx := slices.IndexFunc(tokens, func(t string) bool {
			_, ok := pkgManagers[path.Base(t)]
			return ok
		})
		if pmIdx < 0 {
			continue
		}
		pm := pkgManagers[path.Base(tokens[pmIdx])]
		if pm.libc != libc {
			continue
		}
		seenSubcmd := false
		for i := pmIdx + 1; i < len(tokens); i++ {
			arg := tokens[i]
			if shellRedirectPat.MatchString(arg) {
				if shellRedirectPat.ReplaceAllString(arg, "") == "" {
					i++
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				if slices.Contains(pm.valueFlags, arg) && i+1 < len(tokens) {
					i++
				}
				continue
			}
			if !seenSubcmd {
				seenSubcmd = arg == pm.subcmd
				continue
			}
			if pkg := cleanPackageToken(arg); pkg != "" {
				deps = appendUnique(deps, pkg)
			}
		}
	}
	return deps
}

func cleanPackageToken(tok string) string {
	if strings.Contains(tok, "/") {
		return ""
	}
	switch path.Ext(tok) {
	case ".rpm", ".deb", ".apk":
		return ""
	}
	if idx := strings.IndexAny(tok, "=<>~@"); idx > 0 {
		tok = tok[:idx]
	}
	if strings.ContainsAny(tok, "$`<>();&|*{}! \t") {
		return ""
	}
	return tok
}
