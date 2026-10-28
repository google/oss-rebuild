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
	bash "github.com/msuozzo/bonsai/bonsai-bash"
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
		scriptDeps, err := parsePackageManagerCommands(script, libc)
		if err != nil {
			return nil, errors.Wrapf(err, "parsing cibuildwheel hook in %s", pyprojPath)
		}
		deps = appendUnique(deps, scriptDeps...)
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
	rpmPkgManager = pkgManager{
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
	// commandWrappers run their first argument that is not an option or a
	// variable assignment as a command. Each maps to its options that take a
	// value.
	commandWrappers = map[string][]string{
		"sudo": {"-u", "-g", "-C"},
		"env":  {"-u", "-C"},
		"nice": {"-n"},
		"time": nil,
	}
)

// parsePackageManagerCommands extracts system package names from a shell script
// for the target libc (yum/dnf/microdnf install on glibc, apk add on musl).
func parsePackageManagerCommands(script string, libc platform.LibcImpl) ([]string, error) {
	src := []byte(script)
	root, err := bash.NewParser().Parse(src)
	if err != nil {
		return nil, err
	}
	trailing := trailingArgs(root, src)
	var deps []string
	for cmd := range root.Find(bash.KindCommand) {
		argv := append(commandArgv(cmd, src), trailing[cmd]...)
		i := 0
		for i < len(argv) {
			valueFlags, ok := commandWrappers[path.Base(argv[i])]
			if !ok {
				break
			}
			for i++; i < len(argv) && (strings.HasPrefix(argv[i], "-") || strings.Contains(argv[i], "=")); i++ {
				if slices.Contains(valueFlags, argv[i]) {
					i++
				}
			}
		}
		if i >= len(argv) {
			continue
		}
		pm, ok := pkgManagers[path.Base(argv[i])]
		if !ok || pm.libc != libc {
			continue
		}
		seenSubcmd := false
		for i++; i < len(argv); i++ {
			arg := argv[i]
			if strings.HasPrefix(arg, "-") {
				if slices.Contains(pm.valueFlags, arg) {
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
	return deps, nil
}

// commandArgv returns the name and arguments of a command node, with "" for
// each word that needs expansion.
func commandArgv(cmd *bash.Node, src []byte) []string {
	var argv []string
	for _, c := range cmd.Children {
		if c.Field == bash.FieldName || c.Field == bash.FieldArgument {
			argv = append(argv, shellWord(c, src))
		}
	}
	return argv
}

// trailingArgs maps commands to the arguments that tree-sitter-bash parsed as
// redirect destinations after them.
//
// NOTE: tree-sitter-bash parses every word after a redirect operator as a
// destination, so "yum install -y 2>&1 foo" ends the command at the redirect,
// and a redirect after "a && b" wraps the list rather than b. Destinations past
// a redirect's first are arguments of the last command before it.
// https://github.com/tree-sitter/tree-sitter-bash/issues/233
// https://github.com/tree-sitter/tree-sitter-bash/issues/345
func trailingArgs(root *bash.Node, src []byte) map[*bash.Node][]string {
	args := make(map[*bash.Node][]string)
	for stmt := range root.Find(bash.KindRedirectedStatement) {
		last := stmt.ChildByField(bash.FieldBody)
		for last != nil && last.Kind != bash.KindCommand && len(last.Children) > 0 {
			last = last.Children[len(last.Children)-1]
		}
		if last == nil || last.Kind != bash.KindCommand {
			continue
		}
		for _, redirect := range stmt.Children {
			if redirect.Field != bash.FieldRedirect {
				continue
			}
			target := true
			for _, dest := range redirect.Children {
				if dest.Field != bash.FieldDestination {
					continue
				}
				if !target {
					args[last] = append(args[last], shellWord(dest, src))
				}
				target = false
			}
		}
	}
	return args
}

// shellWord returns the value of a word that needs no expansion, or "".
func shellWord(n *bash.Node, src []byte) string {
	switch n.Kind {
	case bash.KindWord, bash.KindNumber:
		return string(n.Text(src))
	case bash.KindRawString:
		return strings.Trim(string(n.Text(src)), "'")
	case bash.KindCommandName, bash.KindString, bash.KindConcatenation:
		var b strings.Builder
		for _, c := range n.Children {
			if !c.Named {
				continue
			}
			if c.Kind == bash.KindStringContent {
				b.Write(c.Text(src))
				continue
			}
			s := shellWord(c, src)
			if s == "" {
				return ""
			}
			b.WriteString(s)
		}
		return b.String()
	}
	return ""
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
	if strings.ContainsAny(tok, "$`<>;&|*{}! \t") {
		return ""
	}
	return tok
}
