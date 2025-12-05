// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runone

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
			name: "missing api",
			cfg: Config{
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Mode:      "smoketest",
			},
			wantErr: true,
		},
		{
			name: "missing ecosystem",
			cfg: Config{
				API:     "http://test",
				Package: "test",
				Version: "1.0.0",
				Mode:    "smoketest",
			},
			wantErr: true,
		},
		{
			name: "missing package",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Version:   "1.0.0",
				Mode:      "smoketest",
			},
			wantErr: true,
		},
		{
			name: "missing version",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Mode:      "smoketest",
			},
			wantErr: true,
		},
		{
			name: "missing mode",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
			},
			wantErr: true,
		},
		{
			name: "invalid mode",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Mode:      "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid overwrite mode",
			cfg: Config{
				API:           "http://test",
				Ecosystem:     "npm",
				Package:       "test",
				Version:       "1.0.0",
				Mode:          "attest",
				OverwriteMode: "INVALID",
			},
			wantErr: true,
		},
		{
			name: "valid config smoketest",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Mode:      "smoketest",
			},
			wantErr: false,
		},
		{
			name: "valid config attest",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Mode:      "attest",
			},
			wantErr: false,
		},
		{
			name: "valid config analyze",
			cfg: Config{
				API:       "http://test",
				Ecosystem: "npm",
				Package:   "test",
				Version:   "1.0.0",
				Mode:      "analyze",
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
