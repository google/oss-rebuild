// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ini

import (
	"bufio"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/semver"
)

// pythonServerScript runs a persistent server that accepts JSON-encoded inputs
// and returns JSON-encoded parse results, avoiding process creation overhead
const pythonServerScript = `
import configparser
import json
import sys
while True:
  try:
    line = sys.stdin.readline()
    if not line:
      break
    input_data = json.loads(line).get("input", "")
    config = configparser.ConfigParser(
        inline_comment_prefixes=('#', ';'),
        interpolation=None,
        allow_no_value=False,
        allow_unnamed_section=True,
        strict=False,
    )
    config.optionxform = str  # case-sensitive section headers
    try:
      config.read_string(input_data)
      result = {}
      if config.has_section(configparser.UNNAMED_SECTION) and config[configparser.UNNAMED_SECTION]:
        result[''] = dict(config[configparser.UNNAMED_SECTION])
      result |= {section: dict(config[section]) for section in config.sections() if section != configparser.UNNAMED_SECTION}
      response = {"result": result}
    except Exception as e:
      response = {"error": repr(e)}
    print(json.dumps(response), flush=True)
  except Exception as e:
    print(json.dumps({"error": repr(e)}), flush=True)
`

// pythonParser manages a persistent Python process for parsing
type pythonParser struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

// newPythonParser starts a persistent Python server process
func newPythonParser() (*pythonParser, error) {
	cmd := exec.Command("python3", "-c", pythonServerScript)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pythonParser{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
	}, nil
}

// parse sends input to the Python server and receives the parsed result
func (p *pythonParser) parse(input string) (map[string]map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	requestJSON, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(requestJSON, '\n')); err != nil {
		return nil, err
	}
	responseLine, err := p.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response struct {
		Result map[string]map[string]string `json:"result"`
		Error  string                       `json:"error"`
	}
	if err := json.Unmarshal(responseLine, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, &pythonError{response.Error}
	}
	return response.Result, nil
}

// close terminates the Python server process
func (p *pythonParser) close() error {
	p.stdin.Close()
	return p.cmd.Wait()
}

type pythonError struct {
	msg string
}

func (e *pythonError) Error() string {
	return e.msg
}

// parseGo uses our INI parser to parse the input
func parseGo(input string) (map[string]map[string]string, error) {
	file, err := Parse(strings.NewReader(input))
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]string)
	for name, section := range file.Sections {
		// Skip only the default section if it's empty (Python behavior)
		if name == "" && len(section.Values) == 0 {
			continue
		}
		result[name] = section.Values
	}
	return result, nil
}

// hasControlChars checks if a string contains control/non-printable characters
// or the specific Unicode whitespace characters we want to exclude
func hasControlChars(s string) bool {
	for _, r := range s {
		// Exclude specific Unicode whitespace characters that cause byte-vs-rune indent issues
		// These are multi-byte UTF-8 whitespace chars: NEL, NBSP, line/paragraph separators, various spaces
		if r == '\x85' || r == '\xa0' || r == '\u2028' || r == '\u2029' ||
			(r >= '\u2000' && r <= '\u200a') {
			return true
		}
		// Exclude non-printable characters that aren't standard whitespace
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func FuzzCompareWithPython(f *testing.F) {
	if _, err := exec.LookPath("python3"); err != nil {
		f.Skip("python3 not found, skipping python comparison")
	}
	vbytes, err := exec.Command("python3", "--version").Output()
	parts := strings.Split(strings.TrimSpace(string(vbytes)), " ")
	if version := parts[len(parts)-1]; semver.Cmp(version, "3.13.0") < 0 {
		f.Skipf("python version too old (%s < 3.13.0), skipping python comparison", version)
	} else {
		f.Logf("using python version %s for comparison", version)
	}
	{
		// Add initial corpus
		// Basic syntax variations
		f.Add("[section]\nkey=value")
		f.Add("[section]\nkey = value")
		f.Add("[section]\nkey: value")
		f.Add("[section]\nkey =value")
		f.Add("[section]\nkey= value")
		// Metadata examples
		f.Add("[metadata]\nname = package\nversion = 1.0")
		f.Add("[metadata]\nauthor = Jane Doe\nauthor_email = jane@example.com")
		// Multiline values
		f.Add("[options]\ninstall_requires =\n  numpy\n  scipy")
		f.Add("[section]\nlong = value that\n  spans multiple\n  lines")
		f.Add("[metadata]\ndescription = First line\n  second line\n  third line")
		// Comments
		f.Add("# comment\n[section]\nkey = value  # inline")
		f.Add("; semicolon comment\n[section]\nkey = value ; inline")
		f.Add("[section]\n# comment between keys\nkey1 = val1\nkey2 = val2")
		// Empty values and sections
		f.Add("[section]\nempty =")
		f.Add("[section]\nempty = ")
		f.Add("[empty_section]")
		// Multiple sections
		f.Add("[section1]\nkey1 = val1\n\n[section2]\nkey2 = val2")
		f.Add("[s1]\na=1\n[s2]\nb=2\n[s3]\nc=3")
		// Dotted section names
		f.Add("[options.extras_require]\ndev = pytest")
		f.Add("[options.packages.find]\nwhere = src")
		// Edge cases with whitespace
		f.Add("[section]\nkey = value with spaces")
		f.Add("[section]\n  indented_key = value")
		f.Add("[section]\nkey = \n  continuation only")
		// Version specifiers (common in setup.cfg)
		f.Add("[options]\npython_requires = >=3.6")
		f.Add("[options]\ninstall_requires = requests>=2.20.0,<3.0.0")
		// Case sensitivity
		f.Add("[Section]\nKey = Value")
		f.Add("[SECTION]\nKEY = VALUE")
		f.Add("[MixedCase]\nMixedKey = MixedValue")
	}
	// Start persistent Python parser
	parser, err := newPythonParser()
	if err != nil {
		f.Fatalf("Failed to start Python parser: %v", err)
	}
	defer parser.close()
	f.Fuzz(func(t *testing.T, input string) {
		// Skip inputs with invalid UTF-8 to avoid JSON encoding differences
		// between Python and Go
		if !utf8.ValidString(input) {
			t.Skip("Skipping invalid UTF-8 input")
		}
		// Skip inputs containing control characters (non-printable)
		// Python's handling of these is inconsistent and they're not realistic INI content
		if hasControlChars(input) {
			t.Skip("Skipping input with only control characters")
		}
		if strings.HasPrefix(input, "[") && strings.ContainsAny(input, "=:") && !strings.Contains(input, "]") {
			t.Skip("Skipping documented Python parsing oddity")
		}
		pythonResult, pythonErr := parser.parse(input)
		goResult, goErr := parseGo(input)
		if (pythonErr != nil) != (goErr != nil) {
			if goErr != nil {
				t.Errorf("Go failed but Python succeeded\nInput: %q\nPython result: %+v\nGo error: %v",
					input, pythonResult, goErr)
			} else {
				t.Errorf("Python failed but Go succeeded\nInput: %q\nGo result: %+v\nPython error: %v",
					input, goResult, pythonErr)
			}
		} else if pythonErr != nil && goErr != nil {
			return
		} else {
			if diff := cmp.Diff(pythonResult, goResult); diff != "" {
				t.Errorf("Parse results differ (-python +go):\nInput: %q\nDiff:\n%s",
					input, diff)
			}
		}
	})
}
