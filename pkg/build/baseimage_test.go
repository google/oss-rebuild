// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestBaseImageConfig_SelectFor(t *testing.T) {
	cfg := DefaultBaseImageConfig()

	tests := []struct {
		name     string
		input    rebuild.Input
		requires rebuild.RequiredEnv
		want     string
	}{
		{
			name: "requires with BaseImage takes highest precedence",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.PyPI,
				},
			},
			requires: rebuild.RequiredEnv{
				BaseImage: "quay.io/pypa/manylinux2014_x86_64",
			},
			want: "quay.io/pypa/manylinux2014_x86_64",
		},
		{
			name: "empty BaseImage in requires falls back to ecosystem default",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.Debian,
				},
			},
			requires: rebuild.RequiredEnv{
				BaseImage: "",
			},
			want: "docker.io/library/debian:stable-20251103-slim",
		},
		{
			name: "ecosystem override used when BaseImage is empty",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.Debian,
				},
			},
			requires: rebuild.RequiredEnv{},
			want:     "docker.io/library/debian:stable-20251103-slim",
		},
		{
			name: "global default used when ecosystem not configured and BaseImage is empty",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.PyPI,
				},
			},
			requires: rebuild.RequiredEnv{},
			want:     "docker.io/library/alpine:3.21",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.SelectFor(tt.input, tt.requires)
			if got != tt.want {
				t.Errorf("SelectFor() = %v, want %v", got, tt.want)
			}
		})
	}
}
