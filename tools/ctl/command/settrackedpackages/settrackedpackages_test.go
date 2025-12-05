// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package settrackedpackages

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
			name: "missing benchmark",
			cfg: Config{
				BenchmarkPath: "",
				Bucket:        "test-bucket",
			},
			wantErr: true,
		},
		{
			name: "missing bucket",
			cfg: Config{
				BenchmarkPath: "benchmark.json",
				Bucket:        "",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: Config{
				BenchmarkPath: "benchmark.json",
				Bucket:        "test-bucket",
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
