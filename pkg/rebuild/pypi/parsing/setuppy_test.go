// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/internal/textwrap"
)

func TestSetupPyArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []map[string]pyValue
	}{
		{
			name: "NoSetupCall",
			src:  `print("hello")`,
			want: nil,
		},
		{
			name: "StringAndListLiterals",
			src: textwrap.Dedent(`
				setup(
				    name='pkg',
				    version="1.2.3",
				    setup_requires=['wheel', "setuptools_scm[toml]"],
				)`)[1:],
			want: []map[string]pyValue{{
				"name":           {kind: pyString, str: "pkg"},
				"version":        {kind: pyString, str: "1.2.3"},
				"setup_requires": {kind: pyStringList, list: []string{"wheel", "setuptools_scm[toml]"}},
			}},
		},
		{
			name: "QualifiedCallAndModuleLevelNames",
			src: textwrap.Dedent(`
				VERSION = '1.2.3'
				REQS = ('a', 'b')
				setuptools.setup(name='pkg', version=VERSION, setup_requires=REQS)`)[1:],
			want: []map[string]pyValue{{
				"name":           {kind: pyString, str: "pkg"},
				"version":        {kind: pyString, str: "1.2.3"},
				"setup_requires": {kind: pyStringList, list: []string{"a", "b"}},
			}},
		},
		{
			name: "RawTripleQuotedAndConcatenatedStrings",
			src:  `setup(name=r'raw-pkg', version='''1.2.3''', description='part ' "two")`,
			want: []map[string]pyValue{{
				"name":        {kind: pyString, str: "raw-pkg"},
				"version":     {kind: pyString, str: "1.2.3"},
				"description": {kind: pyString, str: "part two"},
			}},
		},
		{
			name: "ComputedValuesStayUnresolved",
			src: textwrap.Dedent(`
				import os
				setup(name=os.environ['NAME'], version=f'{MAJOR}.0', setup_requires=b'wheel')`)[1:],
			want: []map[string]pyValue{{
				"name":           {},
				"version":        {},
				"setup_requires": {},
			}},
		},
		{
			name: "UnresolvableListElementsAreDropped",
			src:  `setup(name='pkg', setup_requires=['wheel', get_extra(), MISSING])`,
			want: []map[string]pyValue{{
				"name":           {kind: pyString, str: "pkg"},
				"setup_requires": {kind: pyStringList, list: []string{"wheel"}},
			}},
		},
		{
			name: "SeveralSetupCallsAllReported",
			src: textwrap.Dedent(`
				if sys.version_info[0] == 2:
				    setup(name='pkg', version='1.0')
				else:
				    setup(name='pkg', version='2.0')`)[1:],
			want: []map[string]pyValue{
				{"name": {kind: pyString, str: "pkg"}, "version": {kind: pyString, str: "1.0"}},
				{"name": {kind: pyString, str: "pkg"}, "version": {kind: pyString, str: "2.0"}},
			},
		},
		{
			name: "PositionalArgsAndUnresolvableSplatContributeNothing",
			src:  `setup('pkg', **kwargs, version='1.0')`,
			want: []map[string]pyValue{{"version": {kind: pyString, str: "1.0"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := setupPyArgs([]byte(tc.src))
			if err != nil {
				t.Fatalf("setupPyArgs() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(pyValue{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("setupPyArgs() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetupPyArgsDictSplat(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []map[string]pyValue
	}{
		{
			name: "DictCallBoundToAName",
			src: textwrap.Dedent(`
				data = dict(
				    name='pkg',
				    version='1.2.3',
				    setup_requires=['wheel'],
				)
				setup(**data)`)[1:],
			want: []map[string]pyValue{{
				"name":           {kind: pyString, str: "pkg"},
				"version":        {kind: pyString, str: "1.2.3"},
				"setup_requires": {kind: pyStringList, list: []string{"wheel"}},
			}},
		},
		{
			name: "DictLiteralBoundToAName",
			src: textwrap.Dedent(`
				kwargs = {'name': 'pkg', "version": '1.2.3', 7: 'dropped'}
				setup(**kwargs)`)[1:],
			want: []map[string]pyValue{{
				"name":    {kind: pyString, str: "pkg"},
				"version": {kind: pyString, str: "1.2.3"},
			}},
		},
		{
			name: "SplatMergedWithExplicitArguments",
			src: textwrap.Dedent(`
				base = dict(name='pkg', version='1.0')
				setup(**base, setup_requires=['wheel'])`)[1:],
			want: []map[string]pyValue{{
				"name":           {kind: pyString, str: "pkg"},
				"version":        {kind: pyString, str: "1.0"},
				"setup_requires": {kind: pyStringList, list: []string{"wheel"}},
			}},
		},
		{
			name: "SplatOfUnresolvableExpressionYieldsNothing",
			src:  `setup(**json.load(open('meta.json')))`,
			want: []map[string]pyValue{{}},
		},
		{
			name: "DictMutatedAfterBindingIsNotFollowed",
			src: textwrap.Dedent(`
				d = dict(name='pkg')
				d['version'] = '1.0'
				setup(**d)`)[1:],
			want: []map[string]pyValue{{"name": {kind: pyString, str: "pkg"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := setupPyArgs([]byte(tc.src))
			if err != nil {
				t.Fatalf("setupPyArgs() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(pyValue{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("setupPyArgs() diff (-want +got):\n%s", diff)
			}
		})
	}
}
