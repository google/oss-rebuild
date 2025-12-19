// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"
)

func TestInferPythonVersion(t *testing.T) {
	tests := []struct {
		name string
		reqs []string
		want string
	}{
		{
			name: "no setuptools",
			reqs: []string{"wheel", "requests"},
			want: "",
		},
		{
			name: "setuptools exactly 60",
			reqs: []string{"setuptools==60.0.0"},
			want: "",
		},
		{
			name: "setuptools greater than 60",
			reqs: []string{"setuptools>=60.0.0"},
			want: "",
		},
		{
			name: "setuptools less than 60",
			reqs: []string{"setuptools<60"},
			want: "3.11",
		},
		{
			name: "setuptools less than or equal 59",
			reqs: []string{"setuptools<=59"},
			want: "3.11",
		},
		{
			name: "setuptools exactly 58",
			reqs: []string{"setuptools==58.1.0"},
			want: "3.11",
		},
		{
			name: "complex constraint matching",
			reqs: []string{"setuptools>=40.0,<60.0"},
			want: "3.11",
		},
		{
			name: "case insensitive",
			reqs: []string{"SetupTools<60"},
			want: "3.11",
		},
		{
			name: "with extras",
			reqs: []string{"setuptools[core]<60"},
			want: "3.11",
		},
		{
			name: "setuptools-scm should not match",
			reqs: []string{"setuptools-scm<60"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferPythonVersion(tt.reqs); got != tt.want {
				t.Errorf("inferPythonVersion() = %v, want %v", got, tt.want)
			}
		})
	}
}
