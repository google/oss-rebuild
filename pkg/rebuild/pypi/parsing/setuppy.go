// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"maps"
	"math"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/msuozzo/bonsai"
	py "github.com/msuozzo/bonsai/bonsai-python"
	"github.com/pkg/errors"
)

// setup.py does not necessarily declare anything statically. Package metadata
// is whatever the setup() call receives when the script is run. We recover the
// common case by resolving the arguments that are literals, or module-level
// names bound to literals, and leave everything computed (file reads, calls,
// os.environ) unresolved.
//
// Arguments assembled into a dict and splatted, setup(**kwargs), are followed
// too. A dict mutated after it is bound, kwargs["name"] = ..., is not: that
// needs the assignments replayed in order, which is interpretation.

type pyKind int

const (
	// pyUnresolved covers both an absent argument and one computed at runtime.
	pyUnresolved pyKind = iota
	pyString
	pyStringList
	pyDict
)

// pyValue is a setup() argument resolved from the AST.
type pyValue struct {
	kind pyKind
	str  string             // set for pyString
	list []string           // set for pyStringList (non-string elements are dropped)
	dict map[string]pyValue // set for pyDict (non-string keys are dropped)
}

// pythonParsers pools parsers across calls. A bonsai parser is not
// goroutine-safe, and instantiating its wasm module costs far more than parsing
// any one setup.py.
var pythonParsers = sync.Pool{New: func() any { return py.NewParser() }}

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

// analyzer accumulates module-level bindings as it descends. Tree shapes
// follow the tree-sitter Python grammar, pinned by bonsai-python to
// tree-sitter-python v0.25.0.
// https://github.com/tree-sitter/tree-sitter-python/blob/v0.25.0/src/node-types.json
type analyzer struct {
	src   []byte
	vars  map[string]pyValue
	calls []map[string]pyValue
}

// walk visits nodes in source order so that a name is bound before the setup()
// calls that reference it.
func (a *analyzer) walk(n *bonsai.Node) {
	switch n.Kind {
	case py.KindAssignment:
		target, value := n.ChildByField(py.FieldLeft), n.ChildByField(py.FieldRight)
		if target != nil && value != nil && target.Kind == py.KindIdentifier {
			a.vars[a.text(target)] = a.resolve(value)
		}
	case py.KindCall:
		if a.isSetupCall(n.ChildByField(py.FieldFunction)) {
			a.calls = append(a.calls, a.keywordArgs(n.ChildByField(py.FieldArguments)))
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
	switch fn.Kind {
	case py.KindIdentifier:
		return a.text(fn) == "setup"
	case py.KindAttribute:
		attr := fn.ChildByField(py.FieldAttribute)
		return attr != nil && a.text(attr) == "setup"
	}
	return false
}

func (a *analyzer) keywordArgs(args *bonsai.Node) map[string]pyValue {
	kwargs := make(map[string]pyValue)
	if args == nil {
		return kwargs
	}
	// Source order, so that a later argument wins. Python rejects a key given
	// both ways outright, so the two cannot legitimately disagree.
	for _, arg := range args.Children {
		switch arg.Kind {
		case py.KindKeywordArgument:
			name, value := arg.ChildByField(py.FieldName), arg.ChildByField(py.FieldValue)
			if name != nil && value != nil {
				kwargs[a.text(name)] = a.resolve(value)
			}
		case py.KindDictionarySplat:
			// setup(**kwargs), where the metadata was assembled into a dict
			// first. Common enough to be worth following.
			for _, c := range arg.Children {
				if !c.Named {
					continue
				}
				if v := a.resolve(c); v.kind == pyDict {
					maps.Copy(kwargs, v.dict)
				}
			}
		}
	}
	return kwargs
}

func (a *analyzer) resolve(n *bonsai.Node) pyValue {
	switch n.Kind {
	case py.KindString:
		if s, ok := a.stringLiteral(n); ok {
			return pyValue{kind: pyString, str: s}
		}
	case py.KindConcatenatedString:
		var s strings.Builder
		for _, part := range n.Children {
			text, ok := a.stringLiteral(part)
			if !ok {
				return pyValue{}
			}
			s.WriteString(text)
		}
		return pyValue{kind: pyString, str: s.String()}
	case py.KindList, py.KindTuple, py.KindSet:
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
	case py.KindDictionary:
		return a.dictValue(n, py.KindPair)
	case py.KindCall:
		// Only dict(...), which is the other half of the setup(**kwargs)
		// idiom. Every other call needs the interpreter.
		if fn := n.ChildByField(py.FieldFunction); fn != nil && fn.Kind == py.KindIdentifier && a.text(fn) == "dict" {
			return a.dictValue(n.ChildByField(py.FieldArguments), py.KindKeywordArgument)
		}
	case py.KindParenthesizedExpression:
		for _, inner := range n.Children {
			if inner.Named {
				return a.resolve(inner)
			}
		}
	case py.KindIdentifier:
		return a.vars[a.text(n)]
	}
	return pyValue{}
}

// dictValue collects the entries of a dict literal or a dict() call. Both spell
// an entry as a keyed child, only the child kind and how the key is written
// differ. Entries whose key is not a plain string are dropped, since a setup()
// argument name is always one.
func (a *analyzer) dictValue(n *bonsai.Node, entry string) pyValue {
	out := map[string]pyValue{}
	if n == nil {
		return pyValue{kind: pyDict, dict: out}
	}
	for _, c := range n.Children {
		if c.Kind != entry {
			continue
		}
		key, value := c.ChildByField(py.FieldKey), c.ChildByField(py.FieldValue)
		if key == nil {
			key = c.ChildByField(py.FieldName) // dict(name=...) spells the key as an identifier
		}
		if key == nil || value == nil {
			continue
		}
		var name string
		switch key.Kind {
		case py.KindIdentifier:
			name = a.text(key)
		case py.KindString:
			s, ok := a.stringLiteral(key)
			if !ok {
				continue
			}
			name = s
		default:
			continue
		}
		out[name] = a.resolve(value)
	}
	return pyValue{kind: pyDict, dict: out}
}

// stringLiteral returns the contents of a string literal. Escape sequences are
// left raw. Nothing in the PEP 508 grammar for names, versions, and dependency
// specifiers requires escaping. https://peps.python.org/pep-0508/#grammar
// f-strings and byte strings have no constant value and are rejected.
func (a *analyzer) stringLiteral(n *bonsai.Node) (string, bool) {
	if n.Kind != py.KindString {
		return "", false
	}
	var prefix string
	var content strings.Builder
	for _, c := range n.Children {
		switch c.Kind {
		case py.KindStringStart:
			// The opening delimiter carries any r/b/f prefix.
			prefix = strings.ToLower(strings.TrimRight(a.text(c), `"'`))
		case py.KindStringContent:
			content.WriteString(a.text(c))
		case py.KindInterpolation:
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

func verifySetupPyFile(ctx context.Context, f *object.File, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = f
	setupPyContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "reading setup.py")
	}
	if filepath.Dir(f.Name) == "." {
		verificationResult.main = true
	}
	setupCalls, err := setupPyArgs([]byte(setupPyContents))
	if err != nil {
		return verificationResult, errors.Wrap(err, "parsing setup.py")
	}
	// A file may hold several setup() calls, typically forked on interpreter
	// version. Score it on whichever one names the closest package.
	closest := math.MaxInt
	for _, args := range setupCalls {
		foundName, ok := args["name"]
		if !ok || foundName.kind != pyString {
			continue
		}
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName.str))
		if editDist >= closest {
			continue
		}
		closest = editDist
		verificationResult.levDistance = editDist
		verificationResult.nameMatch = editDist == 0
		foundVersion, ok := args["version"]
		verificationResult.versionMatch = ok && foundVersion.kind == pyString && foundVersion.str == version
	}
	return verificationResult, nil
}

func extractSetupPyRequirements(ctx context.Context, f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in setup.py")
	setupPyContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "reading setup.py")
	}
	setupCalls, err := setupPyArgs([]byte(setupPyContents))
	if err != nil {
		return nil, errors.Wrap(err, "parsing setup.py")
	}
	for _, args := range setupCalls {
		switch setupRequires := args["setup_requires"]; setupRequires.kind {
		case pyString:
			reqs = append(reqs, setupRequires.str)
		case pyStringList:
			reqs = append(reqs, setupRequires.list...)
		}
	}
	log.Println("Added these reqs from setup.py: " + strings.Join(reqs, ", "))
	return reqs, nil
}
