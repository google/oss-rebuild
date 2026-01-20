// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package benchmark

import "testing"

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{
				Op:       "add",
				InputOne: "input1.json",
				InputTwo: "input2.json",
			},
			wantErr: false,
		},
		{
			name: "invalid operation",
			cfg: Config{
				Op:       "invalid",
				InputOne: "input1.json",
				InputTwo: "input2.json",
			},
			wantErr: true,
		},
		{
			name: "missing input",
			cfg: Config{
				Op:       "add",
				InputOne: "input1.json",
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
