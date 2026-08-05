// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"strings"
	"sync"

	"github.com/msuozzo/bonsai"
	bonsaipython "github.com/msuozzo/bonsai/bonsai-python"
	"github.com/pkg/errors"
)

// setup.py does not necessarily declare anything statically. Package metadata
// is whatever the setup() call receives when the script is run. We recover the
// common case by resolving the arguments that are literals, or module-level
// names bound to literals, and leave everything computed (file reads, calls,
// os.environ) unresolved.

type pyKind int

const (
	// pyUnresolved covers both an absent argument and one computed at runtime.
	pyUnresolved pyKind = iota
	pyString
	pyStringList
)

// pyValue is a setup() argument resolved from the AST.
type pyValue struct {
	kind pyKind
	str  string   // set for pyString
	list []string // set for pyStringList (non-string elements are dropped)
}

// pythonParsers pools parsers across calls. A bonsai parser is not
// goroutine-safe, and instantiating its wasm module costs far more than parsing
// any one setup.py.
var pythonParsers = sync.Pool{New: func() any { return bonsaipython.NewParser() }}

// setupPyArgs returns the keyword arguments of every setup() call in src.
func setupPyArgs(src []byte) ([]map[string]pyValue, error) {
	parser := pythonParsers.Get().(*bonsai.Parser)
	defer pythonParsers.Put(parser)
	root, err := parser.Parse(src)
	if err != nil {
		return nil, errors.Wrap(err, "parsing python")
	}
	a := analyzer{src: src, vars: make(map[string]pyValue)}
	a.walk(root)
	return a.calls, nil
}

// analyzer accumulates module-level bindings as it descends. The node type and
// field names it matches on come from the tree-sitter Python grammar, pinned by
// bonsai-python v0.3.0 to tree-sitter-python v0.25.0.
// https://github.com/tree-sitter/tree-sitter-python/blob/v0.25.0/src/node-types.json
type analyzer struct {
	src   []byte
	vars  map[string]pyValue
	calls []map[string]pyValue
}

// walk visits nodes in source order so that a name is bound before the setup()
// calls that reference it.
func (a *analyzer) walk(n *bonsai.Node) {
	switch n.Type {
	case "assignment":
		target, value := n.ChildByField("left"), n.ChildByField("right")
		if target != nil && value != nil && target.Type == "identifier" {
			a.vars[a.text(target)] = a.resolve(value)
		}
	case "call":
		if a.isSetupCall(n.ChildByField("function")) {
			a.calls = append(a.calls, a.keywordArgs(n.ChildByField("arguments")))
		}
	}
	for _, c := range n.Children {
		a.walk(c)
	}
}

// isSetupCall matches both the bare setup() and the qualified forms
// (setuptools.setup, distutils.core.setup).
func (a *analyzer) isSetupCall(fn *bonsai.Node) bool {
	if fn == nil {
		return false
	}
	switch fn.Type {
	case "identifier":
		return a.text(fn) == "setup"
	case "attribute":
		attr := fn.ChildByField("attribute")
		return attr != nil && a.text(attr) == "setup"
	}
	return false
}

func (a *analyzer) keywordArgs(args *bonsai.Node) map[string]pyValue {
	kwargs := make(map[string]pyValue)
	if args == nil {
		return kwargs
	}
	for _, arg := range args.Children {
		if arg.Type != "keyword_argument" {
			continue
		}
		name, value := arg.ChildByField("name"), arg.ChildByField("value")
		if name != nil && value != nil {
			kwargs[a.text(name)] = a.resolve(value)
		}
	}
	return kwargs
}

func (a *analyzer) resolve(n *bonsai.Node) pyValue {
	switch n.Type {
	case "string":
		if s, ok := a.stringLiteral(n); ok {
			return pyValue{kind: pyString, str: s}
		}
	case "concatenated_string":
		var s strings.Builder
		for _, part := range n.Children {
			text, ok := a.stringLiteral(part)
			if !ok {
				return pyValue{}
			}
			s.WriteString(text)
		}
		return pyValue{kind: pyString, str: s.String()}
	case "list", "tuple", "set":
		// Unresolvable elements are dropped rather than poisoning the whole
		// sequence. A requirement list with one computed entry still tells us
		// about the rest.
		var list []string
		for _, elem := range n.Children {
			if !elem.Named {
				continue
			}
			if v := a.resolve(elem); v.kind == pyString {
				list = append(list, v.str)
			}
		}
		return pyValue{kind: pyStringList, list: list}
	case "parenthesized_expression":
		for _, inner := range n.Children {
			if inner.Named {
				return a.resolve(inner)
			}
		}
	case "identifier":
		return a.vars[a.text(n)]
	}
	return pyValue{}
}

// stringLiteral returns the contents of a string literal. Escape sequences are
// left raw. Nothing in the PEP 508 grammar for names, versions, and dependency
// specifiers requires escaping. https://peps.python.org/pep-0508/#grammar
// f-strings and byte strings have no constant value and are rejected.
func (a *analyzer) stringLiteral(n *bonsai.Node) (string, bool) {
	if n.Type != "string" {
		return "", false
	}
	var prefix string
	var content strings.Builder
	for _, c := range n.Children {
		switch c.Type {
		case "string_start":
			// The opening delimiter carries any r/b/f prefix.
			prefix = strings.ToLower(strings.TrimRight(a.text(c), `"'`))
		case "string_content":
			content.WriteString(a.text(c))
		case "interpolation":
			return "", false
		}
	}
	if strings.ContainsAny(prefix, "bf") {
		return "", false
	}
	return content.String(), true
}

func (a *analyzer) text(n *bonsai.Node) string {
	return string(n.Text(a.src))
}
