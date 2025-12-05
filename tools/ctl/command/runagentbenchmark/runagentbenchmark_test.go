// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runagentbenchmark

import (
	"testing"
)

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "missing project",
			cfg: Config{
				API:           "http://test",
				BenchmarkFile: "benchmark.json",
			},
			wantErr: true,
		},
		{
			name: "missing api",
			cfg: Config{
				Project:       "test-project",
				BenchmarkFile: "benchmark.json",
			},
			wantErr: true,
		},
		{
			name: "missing benchmark file",
			cfg: Config{
				Project: "test-project",
				API:     "http://test",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: Config{
				Project:       "test-project",
				API:           "http://test",
				BenchmarkFile: "benchmark.json",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
