// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"reflect"
	"strings"
	"testing"
)

func TestCustomConfigOneOf_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     CustomConfigOneOf
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid replace pattern",
			cfg: CustomConfigOneOf{
				ReplacePattern: &ReplacePattern{
					Path:    "test/path",
					Pattern: "pattern",
					Replace: "replace",
				},
				Reason: "test reason",
			},
			wantErr: false,
		},
		{
			name: "valid exclude path",
			cfg: CustomConfigOneOf{
				ExcludePath: &ExcludePath{
					Path: "test/path",
				},
				Reason: "test reason",
			},
			wantErr: false,
		},
		{
			name: "missing reason",
			cfg: CustomConfigOneOf{
				ReplacePattern: &ReplacePattern{
					Path:    "test/path",
					Pattern: "pattern",
					Replace: "replace",
				},
			},
			wantErr: true,
			errMsg:  "no reason provided",
		},
		{
			name: "no config provided",
			cfg: CustomConfigOneOf{
				Reason: "test reason",
			},
			wantErr: true,
			errMsg:  "exactly one CustomConfig must be set",
		},
		{
			name: "multiple configs provided",
			cfg: CustomConfigOneOf{
				ReplacePattern: &ReplacePattern{
					Path:    "test/path",
					Pattern: "pattern",
					Replace: "replace",
				},
				ExcludePath: &ExcludePath{
					Path: "test/path",
				},
				Reason: "test reason",
			},
			wantErr: true,
			errMsg:  "exactly one CustomConfig must be set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("Validate() error = %v, want %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestReplacePattern_Validate(t *testing.T) {
	tests := []struct {
		name    string
		rp      ReplacePattern
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid replace pattern",
			rp: ReplacePattern{
				Path:    "test/path",
				Pattern: "pattern",
				Replace: "replace",
			},
			wantErr: false,
		},
		{
			name: "empty path",
			rp: ReplacePattern{
				Pattern: "pattern",
				Replace: "replace",
			},
			wantErr: true,
			errMsg:  "empty path",
		},
		{
			name: "invalid pattern",
			rp: ReplacePattern{
				Path:    "test/path",
				Pattern: "[invalid",
				Replace: "replace",
			},
			wantErr: true,
			errMsg:  "bad pattern",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rp.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, should contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestExcludePath_Validate(t *testing.T) {
	tests := []struct {
		name    string
		ep      ExcludePath
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid exclude path",
			ep: ExcludePath{
				Path: "test/path",
			},
			wantErr: false,
		},
		{
			name: "empty path",
			ep: ExcludePath{
				Path: "",
			},
			wantErr: true,
			errMsg:  "empty path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ep.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate() expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("Validate() error = %v, want %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

// Mock implementation to test ReplacePattern.Stabilizer
func TestReplacePattern_Stabilizer(t *testing.T) {
	rp := &ReplacePattern{
		Path:    "test/path",
		Pattern: "pattern",
		Replace: "replace",
	}
	tests := []struct {
		name     string
		format   Format
		wantType reflect.Type
		wantName string
		wantErr  bool
	}{
		{
			name:     "tar format",
			format:   TarFormat,
			wantType: reflect.TypeOf(TarEntryStabilizer{}),
			wantName: "replace-pattern-test",
		},
		{
			name:     "tgz format",
			format:   TarGzFormat,
			wantType: reflect.TypeOf(TarEntryStabilizer{}),
			wantName: "replace-pattern-test",
		},
		{
			name:     "zip format",
			format:   ZipFormat,
			wantType: reflect.TypeOf(ZipEntryStabilizer{}),
			wantName: "replace-pattern-test",
		},
		{
			name:    "unsupported format",
			format:  UnknownFormat,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizer, err := rp.Stabilizer("test", tt.format)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Stabilizer() expected error, got nil")
				}
				if stabilizer != nil {
					t.Errorf("Stabilizer() expected nil stabilizer, got %v", stabilizer)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stabilizer() unexpected error: %v", err)
			}
			if stabilizer == nil {
				t.Fatalf("Stabilizer() unexpectedly returned nil")
			}
			gotType := reflect.TypeOf(stabilizer)
			if gotType != tt.wantType {
				t.Errorf("Stabilizer() type = %v, want %v", gotType, tt.wantType)
			}
			switch format := tt.format; format {
			case TarFormat, TarGzFormat:
				s := stabilizer.(TarEntryStabilizer)
				if s.Name != tt.wantName {
					t.Errorf("Stabilizer() name = %v, want %v", s.Name, tt.wantName)
				}
				if s.Func == nil {
					t.Errorf("Stabilizer() Func is nil")
				}
			case ZipFormat:
				s := stabilizer.(ZipEntryStabilizer)
				if s.Name != tt.wantName {
					t.Errorf("Stabilizer() name = %v, want %v", s.Name, tt.wantName)
				}
				if s.Func == nil {
					t.Errorf("Stabilizer() Func is nil")
				}
			}
		})
	}
}

// Test ExcludePath.Stabilizer
func TestExcludePath_Stabilizer(t *testing.T) {
	ep := &ExcludePath{
		Path: "test/path",
	}
	tests := []struct {
		name     string
		format   Format
		wantType reflect.Type
		wantName string
		wantErr  bool
	}{
		{
			name:     "tar format",
			format:   TarFormat,
			wantType: reflect.TypeOf(TarArchiveStabilizer{}),
			wantName: "exclude-path-test",
		},
		{
			name:     "tgz format",
			format:   TarGzFormat,
			wantType: reflect.TypeOf(TarArchiveStabilizer{}),
			wantName: "exclude-path-test",
		},
		{
			name:     "zip format",
			format:   ZipFormat,
			wantType: reflect.TypeOf(ZipArchiveStabilizer{}),
			wantName: "exclude-path-test",
		},
		{
			name:    "unsupported format",
			format:  UnknownFormat,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizer, err := ep.Stabilizer("test", tt.format)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Stabilizer() expected error, got nil")
				}
				if stabilizer != nil {
					t.Errorf("Stabilizer() expected nil stabilizer, got %v", stabilizer)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stabilizer() unexpected error: %v", err)
			}
			if stabilizer == nil {
				t.Fatalf("Stabilizer() unexpectedly returned nil")
			}
			gotType := reflect.TypeOf(stabilizer)
			if gotType != tt.wantType {
				t.Errorf("Stabilizer() type = %v, want %v", gotType, tt.wantType)
			}
			switch format := tt.format; format {
			case TarFormat, TarGzFormat:
				s := stabilizer.(TarArchiveStabilizer)
				if s.Name != tt.wantName {
					t.Errorf("Stabilizer() name = %v, want %v", s.Name, tt.wantName)
				}
				if s.Func == nil {
					t.Errorf("Stabilizer() Func is nil")
				}
			case ZipFormat:
				s := stabilizer.(ZipArchiveStabilizer)
				if s.Name != tt.wantName {
					t.Errorf("Stabilizer() name = %v, want %v", s.Name, tt.wantName)
				}
				if s.Func == nil {
					t.Errorf("Stabilizer() Func is nil")
				}
			}
		})
	}
}

func TestCreateCustomStabilizers(t *testing.T) {
	tests := []struct {
		name        string
		configs     []CustomConfigOneOf
		format      Format
		wantLen     int
		wantErr     bool
		errContains string
	}{
		{
			name: "valid configs",
			configs: []CustomConfigOneOf{
				{
					ReplacePattern: &ReplacePattern{
						Path:    "test/path1",
						Pattern: "pattern1",
						Replace: "replace1",
					},
					Reason: "test reason 1",
				},
				{
					ExcludePath: &ExcludePath{
						Path: "test/path2",
					},
					Reason: "test reason 2",
				},
			},
			format:  TarFormat,
			wantLen: 2,
		},
		{
			name: "validation error",
			configs: []CustomConfigOneOf{
				{
					// Missing reason will cause validation error
					ReplacePattern: &ReplacePattern{
						Path:    "test/path",
						Pattern: "pattern",
						Replace: "replace",
					},
				},
			},
			format:      TarFormat,
			wantErr:     true,
			errContains: "no reason provided",
		},
		{
			name: "custom config validation error",
			configs: []CustomConfigOneOf{
				{
					// Empty path will cause validation error
					ReplacePattern: &ReplacePattern{
						Path:    "",
						Pattern: "pattern",
						Replace: "replace",
					},
					Reason: "test reason",
				},
			},
			format:      TarFormat,
			wantErr:     true,
			errContains: "empty path",
		},
		{
			name: "stabilizer creation error",
			configs: []CustomConfigOneOf{
				{
					ReplacePattern: &ReplacePattern{
						Path:    "test/path",
						Pattern: "pattern",
						Replace: "replace",
					},
					Reason: "", // This will cause an error in Stabilizer()
				},
			},
			format:      TarFormat,
			wantErr:     true,
			errContains: "no reason provided",
		},
		{
			name:    "empty configs",
			configs: []CustomConfigOneOf{},
			format:  TarFormat,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizers, err := CreateCustomStabilizers(tt.configs, tt.format)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CreateCustomStabilizers() expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CreateCustomStabilizers() error = %v, should contain %v", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateCustomStabilizers() unexpected error: %v", err)
			}
			if len(stabilizers) != tt.wantLen {
				t.Errorf("CreateCustomStabilizers() returned %d stabilizers, want %d", len(stabilizers), tt.wantLen)
			}
			// Check that stabilizers are of the correct type based on their format
			for i, cfg := range tt.configs {
				if i >= len(stabilizers) {
					break
				}
				switch {
				case cfg.ReplacePattern != nil:
					switch tt.format {
					case TarFormat, TarGzFormat:
						_, ok := stabilizers[i].(TarEntryStabilizer)
						if !ok {
							t.Errorf("Stabilizer at index %d is not a TarEntryStabilizer", i)
						}
					case ZipFormat:
						_, ok := stabilizers[i].(ZipEntryStabilizer)
						if !ok {
							t.Errorf("Stabilizer at index %d is not a ZipEntryStabilizer", i)
						}
					}
				case cfg.ExcludePath != nil:
					switch tt.format {
					case TarFormat, TarGzFormat:
						_, ok := stabilizers[i].(TarArchiveStabilizer)
						if !ok {
							t.Errorf("Stabilizer at index %d is not a TarArchiveStabilizer", i)
						}
					case ZipFormat:
						_, ok := stabilizers[i].(ZipArchiveStabilizer)
						if !ok {
							t.Errorf("Stabilizer at index %d is not a ZipArchiveStabilizer", i)
						}
					}
				}
			}
		})
	}
}

// Test for CustomConfigOneOf.CustomConfig method
func TestCustomConfigOneOf_CustomConfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      CustomConfigOneOf
		wantType reflect.Type
		wantNil  bool
	}{
		{
			name: "replace pattern",
			cfg: CustomConfigOneOf{
				ReplacePattern: &ReplacePattern{
					Path:    "test/path",
					Pattern: "pattern",
					Replace: "replace",
				},
				Reason: "test reason",
			},
			wantType: reflect.TypeOf(&ReplacePattern{}),
			wantNil:  false,
		},
		{
			name: "exclude path",
			cfg: CustomConfigOneOf{
				ExcludePath: &ExcludePath{
					Path: "test/path",
				},
				Reason: "test reason",
			},
			wantType: reflect.TypeOf(&ExcludePath{}),
			wantNil:  false,
		},
		{
			name: "no config",
			cfg: CustomConfigOneOf{
				Reason: "test reason",
			},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.CustomConfig()
			if tt.wantNil {
				if got != nil {
					t.Errorf("CustomConfig() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("CustomConfig() unexpectedly returned nil")
			}
			gotType := reflect.TypeOf(got)
			if gotType != tt.wantType {
				t.Errorf("CustomConfig() type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}
