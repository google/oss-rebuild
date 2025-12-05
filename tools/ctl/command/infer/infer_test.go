// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{
				Ecosystem: "npm",
				Package:   "test-package",
				Version:   "1.0.0",
			},
			wantErr: false,
		},
		{
			name: "missing ecosystem",
			cfg: Config{
				Ecosystem: "",
				Package:   "test-package",
				Version:   "1.0.0",
			},
			wantErr: true,
		},
		{
			name: "missing package",
			cfg: Config{
				Ecosystem: "npm",
				Package:   "",
				Version:   "1.0.0",
			},
			wantErr: true,
		},
		{
			name: "missing version",
			cfg: Config{
				Ecosystem: "npm",
				Package:   "test-package",
				Version:   "",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHandler(t *testing.T) {
	// TODO: Test handler behavior
}
