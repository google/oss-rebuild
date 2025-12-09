// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package getgradlegav

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
				Repository: "https://github.com/example/repo",
				Ref:        "abc123",
			},
			wantErr: false,
		},
		{
			name: "missing repository",
			cfg: Config{
				Repository: "",
				Ref:        "main",
			},
			wantErr: true,
		},
		{
			name: "missing ref",
			cfg: Config{
				Repository: "https://github.com/example/repo",
				Ref:        "",
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
