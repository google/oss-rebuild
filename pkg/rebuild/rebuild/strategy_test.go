// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import "testing"

func TestLocationValidate(t *testing.T) {
	tests := []struct {
		name    string
		loc     Location
		wantErr bool
	}{
		{name: "root", loc: Location{}},
		{name: "dot", loc: Location{Dir: "."}},
		{name: "nested", loc: Location{Dir: "packages/@scope/foo-bar_baz.js"}},
		{name: "parent", loc: Location{Dir: "../other"}, wantErr: true},
		{name: "inner_parent", loc: Location{Dir: "a/../../b"}, wantErr: true},
		{name: "absolute", loc: Location{Dir: "/etc"}, wantErr: true},
		{name: "trailing_slash", loc: Location{Dir: "pkg/"}, wantErr: true},
		{name: "dot_prefix", loc: Location{Dir: "./pkg"}, wantErr: true},
		{name: "flag", loc: Location{Dir: "--outdir=/out"}, wantErr: true},
		{name: "space", loc: Location{Dir: "pkg -Dskip=true"}, wantErr: true},
		{name: "command_substitution", loc: Location{Dir: "$(id)"}, wantErr: true},
		{name: "separator", loc: Location{Dir: "pkg;id"}, wantErr: true},
		{name: "template", loc: Location{Dir: "{{cookiecutter.package_name}}"}, wantErr: true},
		{name: "submodule_with_space", loc: Location{Submodules: []Submodule{{Dir: "assets/syntaxes/02_Extra/Assembly (ARM)"}}}},
		{name: "submodule_empty", loc: Location{Submodules: []Submodule{{Dir: ""}}}, wantErr: true},
		{name: "submodule_parent", loc: Location{Submodules: []Submodule{{Dir: "../x"}}}, wantErr: true},
		{name: "submodule_quote", loc: Location{Submodules: []Submodule{{Dir: "x'; id; '"}}}, wantErr: true},
		{name: "submodule_newline", loc: Location{Submodules: []Submodule{{Dir: "x\nEOF"}}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.loc.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
