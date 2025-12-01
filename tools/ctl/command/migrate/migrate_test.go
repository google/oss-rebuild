// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package migrate

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
				Project:       "",
				MigrationName: "test",
			},
			wantErr: true,
		},
		{
			name: "missing migration name",
			cfg: Config{
				Project:       "test-project",
				MigrationName: "",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: Config{
				Project:       "test-project",
				MigrationName: "test",
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
