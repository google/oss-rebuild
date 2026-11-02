// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"maps"
	"path"
	re "regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	bash "github.com/msuozzo/bonsai/bonsai-bash"
	"github.com/msuozzo/go-git2"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const workflowsDir = ".github/workflows"

// cibuildwheelProjectDir is where cibuildwheel copies the project to in Linux
// build containers.
// https://cibuildwheel.pypa.io/en/stable/platforms/#linux-containers
const cibuildwheelProjectDir = "/project"

var (
	// cibuildwheelActionPat matches a step using the cibuildwheel action by a
	// release tag (v2.16.2), floating minor tag (v2.16), or commit hash.
	cibuildwheelActionPat = re.MustCompile(`^pypa/cibuildwheel@v?(\d+\.\d+(?:\.\d+)?|[0-9a-f]{40})$`)
	// cibuildwheelReqPat matches an exact cibuildwheel requirement in a command,
	// such as "pipx run cibuildwheel==2.16.2".
	cibuildwheelReqPat = re.MustCompile(`\bcibuildwheel(?:\[[^\]]*\])?\s*==\s*(\d+\.\d+\.\d+)\b`)
	// cibuildwheelCmdPat matches a command that names cibuildwheel, such as
	// "pipx run cibuildwheel" or "python -m cibuildwheel".
	cibuildwheelCmdPat = re.MustCompile(`\bcibuildwheel\b`)
	// packageSpecPat matches a package name with an optional version pin, such
	// as "graphviz-dev=14.1.5-r0" or "cairo-dev>=1.18".
	packageSpecPat = re.MustCompile(`^(?P<pkg>[\w.+:()-]+)(?:(?P<op>[<>~=]=?|=~)[\w.+:-]+)?$`)
)

// ghaWorkflow holds the parts of a GitHub Actions workflow that can run or pin
// cibuildwheel.
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
// workflows in tree, and reports whether any step runs cibuildwheel (including
// unpinned uses).
func extractCibuildwheelSpecs(tree *git2.Tree) (specs []string, usesCibuildwheel bool, err error) {
	workflows, err := tree.Tree(workflowsDir)
	if errors.Is(err, git2.ErrDirectoryNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, errors.Wrapf(err, "finding %s", workflowsDir)
	}
	for _, entry := range workflows.Entries {
		if ext := path.Ext(entry.Name); !entry.Mode.IsFile() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		f, err := workflows.TreeEntryFile(&entry)
		if err != nil {
			return nil, false, errors.Wrapf(err, "finding workflow %s", entry.Name)
		}
		contents, err := f.Contents()
		if err != nil {
			return nil, false, errors.Wrapf(err, "reading workflow %s", entry.Name)
		}
		s, u := cibuildwheelSpecs([]byte(contents))
		specs = append(specs, s...)
		usesCibuildwheel = usesCibuildwheel || u
	}
	return specs, usesCibuildwheel, nil
}

func cibuildwheelSpecs(workflow []byte) (specs []string, usesCibuildwheel bool) {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		return nil, false
	}
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		for _, step := range wf.Jobs[name].Steps {
			uses := strings.TrimSpace(step.Uses)
			if m := cibuildwheelActionPat.FindStringSubmatch(uses); m != nil {
				specs = append(specs, m[1])
			}
			for _, m := range cibuildwheelReqPat.FindAllStringSubmatch(step.Run, -1) {
				specs = append(specs, m[1])
			}
			if strings.HasPrefix(uses, "pypa/cibuildwheel@") || cibuildwheelCmdPat.MatchString(step.Run) {
				usesCibuildwheel = true
			}
		}
	}
	return specs, usesCibuildwheel
}

// extractCibuildwheelDependencies extracts system dependencies from
// [tool.cibuildwheel] hooks and overrides in pyproject.toml under searchDir.
func extractCibuildwheelDependencies(tree *git2.Tree, searchDir string, tags WheelTags, libc platform.LibcImpl) ([]string, error) {
	cfg, pyprojPath, err := readCibuildwheelConfig(tree, searchDir)
	if err != nil {
		return nil, err
	}
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
		if overrideAppliesToTarget(ov.Select, tags, libc) {
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

// extractCibuildwheelEnvironment extracts build environment variables from
// [tool.cibuildwheel] tables and matching overrides in pyproject.toml under
// searchDir, with linux overriding top-level entries and matching overrides
// overriding earlier values in order.
func extractCibuildwheelEnvironment(tree *git2.Tree, searchDir string, tags WheelTags, libc platform.LibcImpl) (map[string]string, error) {
	cfg, _, err := readCibuildwheelConfig(tree, searchDir)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	maps.Copy(env, parseCibuildwheelEnvironment(cfg.Environment))
	maps.Copy(env, parseCibuildwheelEnvironment(cfg.Linux.Environment))
	for _, ov := range cfg.Overrides {
		if overrideAppliesToTarget(ov.Select, tags, libc) {
			maps.Copy(env, parseCibuildwheelEnvironment(ov.Environment))
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

func readCibuildwheelConfig(tree *git2.Tree, searchDir string) (parsing.CibuildwheelConfig, string, error) {
	if tree == nil {
		return parsing.CibuildwheelConfig{}, "", nil
	}
	pyprojPath := path.Clean(path.Join(searchDir, "pyproject.toml"))
	f, err := tree.File(pyprojPath)
	if errors.Is(err, git2.ErrFileNotFound) {
		return parsing.CibuildwheelConfig{}, pyprojPath, nil
	} else if err != nil {
		return parsing.CibuildwheelConfig{}, pyprojPath, errors.Wrapf(err, "finding %s", pyprojPath)
	}
	pyProject, err := parsing.ReadPyProject(f)
	if err != nil {
		// A pyproject.toml that fails TOML decoding is a rejected candidate, not an infrastructure failure.
		var decodeErr *toml.DecodeError
		if errors.As(err, &decodeErr) {
			return parsing.CibuildwheelConfig{}, pyprojPath, nil
		}
		return parsing.CibuildwheelConfig{}, pyprojPath, errors.Wrapf(err, "reading %s", pyprojPath)
	}
	return pyProject.Tool.Cibuildwheel, pyprojPath, nil
}

func parseCibuildwheelEnvironment(raw any) map[string]string {
	switch v := raw.(type) {
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, item := range v {
			if val, ok := tomlScalarString(item); ok && isSafeCibuildwheelEnv(k, val) {
				out[k] = val
			}
		}
		return out
	case string:
		src := []byte(v)
		root, err := bash.NewParser().Parse(src)
		if err != nil {
			return nil
		}
		out := make(map[string]string)
		for assign := range root.Find(bash.KindVariableAssignment) {
			nameNode := assign.ChildByField(bash.FieldName)
			if nameNode == nil {
				continue
			}
			key := string(nameNode.Text(src))
			valNode := assign.ChildByField(bash.FieldValue)
			val := ""
			if valNode != nil {
				var ok bool
				val, ok = shellLiteral(valNode, src)
				if !ok {
					continue
				}
			}
			if isSafeCibuildwheelEnv(key, val) {
				out[key] = val
			}
		}
		return out
	default:
		return nil
	}
}

func tomlScalarString(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case int64:
		return strconv.FormatInt(v, 10), true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		return "", false
	}
}

var (
	// blockedEnvPrefixes is an empirical blocklist of installer, wheel-repair,
	// CI-runner, Rust-network, macOS, and test-driver prefixes that either conflict
	// with offline rebuild phases or carry host-specific state.
	blockedEnvPrefixes = []string{
		"PIP_", "UV_", "AUDITWHEEL_", "CIBW_",
		"RUNNER_", "GITHUB_", "CARGO_", "RUSTUP_",
		"MACOSX_", "CMAKE_OSX_", "SDL_",
	}
	// blockedEnvNames is an empirical blocklist of search-path variables that
	// depend on unexecuted before-all hooks as well as CI terminal, color, and
	// runtime diagnostic settings that do not configure extension compilation.
	blockedEnvNames = []string{
		"PATH", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "LIBRARY_PATH",
		"CPATH", "C_INCLUDE_PATH", "CPLUS_INCLUDE_PATH", "PKG_CONFIG_PATH",
		"CI", "ARCHFLAGS",
		"COLOR", "FORCE_COLOR", "NO_COLOR", "PY_COLORS",
		"CLICOLOR", "CLICOLOR_FORCE", "MYPY_FORCE_COLOR", "PRE_COMMIT_COLOR",
		"COLUMNS", "LINES", "TERM",
		"PYTHONUNBUFFERED", "PYTHONDEVMODE",
	}
)

func isSafeCibuildwheelEnv(key, val string) bool {
	if !buildEnvNamePat.MatchString(key) || !buildEnvValuePat.MatchString(val) || slices.Contains(blockedEnvNames, key) {
		return false
	}
	return !slices.ContainsFunc(blockedEnvPrefixes, func(p string) bool {
		return strings.HasPrefix(key, p)
	})
}

func tomlStrings(v any) (out []string) {
	switch v := v.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return []string{v}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// maxBraceExpansions and maxSelectorLen bound the work brace expansion does on
// a select pattern from an untrusted pyproject.toml.
const (
	maxBraceExpansions = 1024
	maxSelectorLen     = 256
)

func overrideAppliesToTarget(selectVal any, tags WheelTags, libc platform.LibcImpl) bool {
	if selectVal == nil {
		return true
	}
	selectors := tomlStrings(selectVal)
	buildIDs := cibuildwheelBuildIDs(tags, libc)
	// NOTE: cibuildwheel expands "{a,b}" braces before fnmatch and fnmatch
	// negates character classes with "!" instead of "^".
	return slices.ContainsFunc(selectors, func(sel string) bool {
		return slices.ContainsFunc(strings.Fields(strings.ToLower(sel)), func(pat string) bool {
			for _, expanded := range expandBraces(pat) {
				glob := strings.ReplaceAll(expanded, "[!", "[^")
				if slices.ContainsFunc(buildIDs, func(id string) bool {
					matched, err := path.Match(glob, id)
					return err == nil && matched
				}) {
					return true
				}
			}
			return false
		})
	})
}

// NOTE: platform.SelectBaseImage enforces x86_64 as the only supported architecture.
// Generic "py3-none-manylinux*" wheel tags produce "py3-<os>_x86_64" identifiers and
// skip "cp*" overrides, failing open to fewer inferred dependencies.
func cibuildwheelBuildIDs(tags WheelTags, libc platform.LibcImpl) []string {
	var osName string
	switch libc {
	case platform.Glibc:
		osName = "manylinux"
	case platform.Musl:
		osName = "musllinux"
	default:
		return nil
	}
	var ids []string
	for pyTag := range strings.SplitSeq(strings.ToLower(cibuildwheelPythonTag(tags)), ".") {
		if pyTag != "" {
			ids = append(ids, pyTag+"-"+osName+"_x86_64")
		}
	}
	return ids
}

// cibuildwheelPythonTag returns the Python selector prefix cibuildwheel uses in
// build identifiers for a wheel's Python and ABI tags, such as "cp313t" for
// free-threaded CPython wheels and "cp310" otherwise.
func cibuildwheelPythonTag(tags WheelTags) string {
	if strings.HasPrefix(tags.ABI, "cp") && strings.HasSuffix(tags.ABI, "t") {
		return tags.ABI
	}
	return tags.Python
}

// expandBraces expands the unnested "{a,b}" groups in pat.
func expandBraces(pat string) []string {
	pre, rest, ok := strings.Cut(pat, "{")
	group, post, closed := strings.Cut(rest, "}")
	if !ok || !closed || len(pat) > maxSelectorLen {
		return []string{pat}
	}
	tails := expandBraces(post)
	var out []string
	for _, alt := range strings.Split(group, ",") {
		for _, tail := range tails {
			if len(out) == maxBraceExpansions {
				return out
			}
			out = append(out, pre+alt+tail)
		}
	}
	return out
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
	s, _ := shellLiteral(n, src)
	return s
}

// shellLiteral returns the unquoted value of a literal shell word and true, or
// ("", false) when the node contains an expansion or substitution.
func shellLiteral(n *bash.Node, src []byte) (string, bool) {
	switch n.Kind {
	case bash.KindWord, bash.KindNumber:
		return string(n.Text(src)), true
	case bash.KindRawString:
		return strings.TrimSuffix(strings.TrimPrefix(string(n.Text(src)), "'"), "'"), true
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
			s, ok := shellLiteral(c, src)
			if !ok {
				return "", false
			}
			b.WriteString(s)
		}
		return b.String(), true
	}
	return "", false
}

// cleanPackageToken returns the package spec to install or "" when the token
// is not a plain package operand. Tokens with an "@" tag are rejected rather
// than truncated.
func cleanPackageToken(tok string) string {
	switch path.Ext(tok) {
	case ".rpm", ".deb", ".apk":
		return ""
	}
	if !packageSpecPat.MatchString(tok) {
		return ""
	}
	return tok
}
