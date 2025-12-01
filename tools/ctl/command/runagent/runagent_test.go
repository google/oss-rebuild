// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runagent

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
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Artifact:  "test.tgz",
			},
			wantErr: true,
		},
		{
			name: "missing api",
			cfg: Config{
				Project:   "test-project",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Artifact:  "test.tgz",
			},
			wantErr: true,
		},
		{
			name: "missing ecosystem",
			cfg: Config{
				Project:  "test-project",
				API:      "http://test",
				Package:  "test",
				Version:  "1.0.0",
				Artifact: "test.tgz",
			},
			wantErr: true,
		},
		{
			name: "missing package",
			cfg: Config{
				Project:   "test-project",
				API:       "http://test",
				Ecosystem: "npm",
				Version:   "1.0.0",
				Artifact:  "test.tgz",
			},
			wantErr: true,
		},
		{
			name: "missing version",
			cfg: Config{
				Project:   "test-project",
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Artifact:  "test.tgz",
			},
			wantErr: true,
		},
		{
			name: "missing artifact",
			cfg: Config{
				Project:   "test-project",
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: Config{
				Project:   "test-project",
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Artifact:  "test.tgz",
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
