// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

var lineContinuationPattern = regexp.MustCompile(`\\\r?\n`)

// ParsePackageManagerCommands extracts system package DependencyIdentifiers from a shell script.
// It scans for yum, dnf, microdnf, apk, apt-get, and apt installation commands.
func ParsePackageManagerCommands(script, provenance string) []DependencyIdentifier {
	normalized := lineContinuationPattern.ReplaceAllString(script, " ")
	commands := splitShellCommands(normalized)
	var ids []DependencyIdentifier
	for _, com := range commands {
		tokens, err := shlex.Split(com)
		if err != nil || len(tokens) == 0 {
			tokens = strings.Fields(com)
		}
		ids = append(ids, parseCommandTokens(tokens, provenance)...)
	}
	return DeduplicateIdentifiers(ids)
}

// splitShellCommands is a best-effort minimal shell tokenizer that isolates individual commands from a script.
//
// An individual command is considered a single invocation of a program including its arguments.
// The tokenizer applies the following heuristics:
// - Splits on unquoted command separators: `\n`, `;`, `&&`, `||`, `|`, `(`, and `)`.
// - Preserves quoted separators inside single quotes ('...') and double quotes ("...").
// - Respects backslash escapes outside single quotes, including line continuations (`\` followed by `\n`).
// - Strips shell comments (`#`) when preceded by whitespace, a newline, a semicolon, or at the start of the line.
func splitShellCommands(script string) []string {
	var segments []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	for i := 0; i < len(script); i++ {
		ch := script[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingleQuote {
			current.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			current.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			current.WriteByte(ch)
			continue
		}
		if !inSingleQuote && !inDoubleQuote {
			if ch == '#' && (i == 0 || script[i-1] == ' ' || script[i-1] == '\t' || script[i-1] == '\n' || script[i-1] == ';') {
				for i < len(script) && script[i] != '\n' {
					i++
				}
				segments = appendSegment(segments, current.String())
				current.Reset()
				continue
			}
			if ch == '\n' || ch == ';' || ch == '(' || ch == ')' {
				segments = appendSegment(segments, current.String())
				current.Reset()
				continue
			}
			if (ch == '&' || ch == '|') && i+1 < len(script) && script[i+1] == ch {
				segments = appendSegment(segments, current.String())
				current.Reset()
				i++
				continue
			}
			if ch == '|' {
				segments = appendSegment(segments, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteByte(ch)
	}
	segments = appendSegment(segments, current.String())
	return segments
}

func appendSegment(segments []string, s string) []string {
	trimmed := strings.TrimSpace(s)
	if trimmed != "" {
		return append(segments, trimmed)
	}
	return segments
}

// parseCommandTokens parses a list of (shell) tokens into a list of DependencyIdentifiers
//
// The tokens are essentially the elements of an argv array
func parseCommandTokens(tokens []string, provenance string) []DependencyIdentifier {
	idx := skipCommandPrefixes(tokens)
	if idx >= len(tokens) {
		return nil
	}
	cmd := filepath.Base(tokens[idx])
	args := tokens[idx+1:]
	switch cmd {
	case "yum":
		return extractRPMInstallArgs(args, NamespaceYum, provenance)
	case "dnf", "microdnf":
		return extractRPMInstallArgs(args, NamespaceDnf, provenance)
	case "apk":
		return extractApkAddArgs(args, provenance)
	case "apt-get", "apt":
		return extractAptInstallArgs(args, provenance)
	default:
		return nil
	}
}

// skipCommandPrefixes identifies common shell prefixes before a command and returns the index at which they are over
//
// The common shell prefix patterns it detects are:
// - Environment variable assignments
// - `sudo`, `env`, `time` or `nice` wrappers
func skipCommandPrefixes(tokens []string) int {
	i := 0
	for i < len(tokens) {
		tok := tokens[i]
		if isEnvAssignment(tok) {
			i++
			continue
		}
		base := filepath.Base(tok)
		if base == "sudo" || base == "env" || base == "time" || base == "nice" {
			i++
			for i < len(tokens) && strings.HasPrefix(tokens[i], "-") {
				if (tokens[i] == "-u" || tokens[i] == "-g" || tokens[i] == "-C") && i+1 < len(tokens) {
					i += 2
					continue
				}
				i++
			}
			continue
		}
		break
	}
	return i
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i := range eq {
		c := tok[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) || c == '_') {
			return false
		}
	}
	return true
}

func extractRPMInstallArgs(args []string, ns SourceNamespace, provenance string) []DependencyIdentifier {
	var pkgs []string
	seenInstall := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if rpmFlagTakesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if !seenInstall {
			if arg == "install" {
				seenInstall = true
			}
			continue
		}
		if pkg := cleanPackageToken(arg, false); pkg != "" {
			pkgs = append(pkgs, pkg)
		}
	}
	return toIdentifiers(pkgs, ns, provenance)
}

func rpmFlagTakesValue(flag string) bool {
	switch flag {
	case "-c", "--config", "-e", "--errorlevel", "-d", "--debuglevel",
		"--installroot", "--enablerepo", "--disablerepo", "--exclude", "-x",
		"--setopt", "--releasever", "--repofrompath", "--enableplugin", "--disableplugin":
		return true
	default:
		return false
	}
}

func extractApkAddArgs(args []string, provenance string) []DependencyIdentifier {
	var pkgs []string
	seenAdd := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if apkFlagTakesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if !seenAdd {
			if arg == "add" {
				seenAdd = true
			}
			continue
		}
		if pkg := cleanPackageToken(arg, true); pkg != "" {
			pkgs = append(pkgs, pkg)
		}
	}
	return toIdentifiers(pkgs, NamespaceApk, provenance)
}

func apkFlagTakesValue(flag string) bool {
	switch flag {
	case "-t", "--virtual", "-p", "--root", "-X", "--repository",
		"--keys-dir", "--repositories-file", "--arch", "--cache-dir", "--wait":
		return true
	default:
		return false
	}
}

func extractAptInstallArgs(args []string, provenance string) []DependencyIdentifier {
	var pkgs []string
	seenInstall := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if aptFlagTakesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if !seenInstall {
			if arg == "install" {
				seenInstall = true
			}
			continue
		}
		if pkg := cleanPackageToken(arg, true); pkg != "" {
			pkgs = append(pkgs, pkg)
		}
	}
	return toIdentifiers(pkgs, NamespaceApt, provenance)
}

func aptFlagTakesValue(flag string) bool {
	switch flag {
	case "-o", "--option", "-c", "--config-file", "-t", "--target-release", "--default-release":
		return true
	default:
		return false
	}
}

func cleanPackageToken(tok string, stripSlashRelease bool) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	if strings.ContainsAny(tok, "$`<>();&|*") {
		return ""
	}
	if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		return ""
	}
	if strings.HasSuffix(tok, ".rpm") || strings.HasSuffix(tok, ".deb") || strings.HasSuffix(tok, ".apk") {
		return ""
	}
	if idx := strings.IndexAny(tok, "=<>~"); idx > 0 {
		tok = tok[:idx]
	}
	if idx := strings.IndexByte(tok, '@'); idx > 0 {
		tok = tok[:idx]
	}
	if stripSlashRelease {
		if idx := strings.IndexByte(tok, '/'); idx > 0 {
			tok = tok[:idx]
		}
	}
	return tok
}

func toIdentifiers(pkgs []string, ns SourceNamespace, provenance string) []DependencyIdentifier {
	var ids []DependencyIdentifier
	for _, pkg := range pkgs {
		ids = append(ids, DependencyIdentifier{
			Namespace:  ns,
			Name:       pkg,
			Provenance: provenance,
		})
	}
	return ids
}
