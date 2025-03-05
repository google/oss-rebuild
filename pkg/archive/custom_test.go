// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCustomStabilizerEntry_Validate(t *testing.T) {
	tests := []struct {
		name    string
		entry   CustomStabilizerEntry
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid replace pattern",
			entry: CustomStabilizerEntry{
				Config: CustomStabilizerConfigOneOf{
					ReplacePattern: &ReplacePattern{
						Path:    "test/path",
						Pattern: "pattern",
						Replace: "replace",
					},
				},
				Reason: "test reason",
			},
			wantErr: false,
		},
		{
			name: "valid exclude path",
			entry: CustomStabilizerEntry{
				Config: CustomStabilizerConfigOneOf{
					ExcludePath: &ExcludePath{
						Path: "test/path",
					},
				},
				Reason: "test reason",
			},
			wantErr: false,
		},
		{
			name: "missing reason",
			entry: CustomStabilizerEntry{
				Config: CustomStabilizerConfigOneOf{
					ReplacePattern: &ReplacePattern{
						Path:    "test/path",
						Pattern: "pattern",
						Replace: "replace",
					},
				},
			},
			wantErr: true,
			errMsg:  "no reason provided",
		},
		{
			name: "no config provided",
			entry: CustomStabilizerEntry{
				Config: CustomStabilizerConfigOneOf{},
				Reason: "test reason",
			},
			wantErr: true,
			errMsg:  "exactly one config must be set",
		},
		{
			name: "multiple configs provided",
			entry: CustomStabilizerEntry{
				Config: CustomStabilizerConfigOneOf{
					ReplacePattern: &ReplacePattern{
						Path:    "test/path",
						Pattern: "pattern",
						Replace: "replace",
					},
					ExcludePath: &ExcludePath{
						Path: "test/path",
					},
				},
				Reason: "test reason",
			},
			wantErr: true,
			errMsg:  "exactly one config must be set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entry.Validate()
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
		entries     []CustomStabilizerEntry
		format      Format
		wantLen     int
		wantErr     bool
		errContains string
	}{
		{
			name: "valid configs",
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/path1",
							Pattern: "pattern1",
							Replace: "replace1",
						},
					},
					Reason: "test reason 1",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Path: "test/path2",
						},
					},
					Reason: "test reason 2",
				},
			},
			format:  TarFormat,
			wantLen: 2,
		},
		{
			name: "validation error",
			entries: []CustomStabilizerEntry{
				{
					// Missing reason will cause validation error
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/path",
							Pattern: "pattern",
							Replace: "replace",
						},
					},
				},
			},
			format:      TarFormat,
			wantErr:     true,
			errContains: "no reason provided",
		},
		{
			name: "custom config validation error",
			entries: []CustomStabilizerEntry{
				{
					// Empty path will cause validation error
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "",
							Pattern: "pattern",
							Replace: "replace",
						},
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
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/path",
							Pattern: "pattern",
							Replace: "replace",
						},
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
			entries: []CustomStabilizerEntry{},
			format:  TarFormat,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizers, err := CreateCustomStabilizers(tt.entries, tt.format)
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
			for i, ent := range tt.entries {
				if i >= len(stabilizers) {
					break
				}
				switch {
				case ent.Config.ReplacePattern != nil:
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
				case ent.Config.ExcludePath != nil:
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

// Test for CustomStabilizerConfigOneOf.CustomStabilizerConfig method
func TestCustomStabilizerConfigOneOf_CustomStabilizerConfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      CustomStabilizerConfigOneOf
		wantType reflect.Type
		wantNil  bool
	}{
		{
			name: "replace pattern",
			cfg: CustomStabilizerConfigOneOf{
				ReplacePattern: &ReplacePattern{
					Path:    "test/path",
					Pattern: "pattern",
					Replace: "replace",
				},
			},
			wantType: reflect.TypeOf(&ReplacePattern{}),
			wantNil:  false,
		},
		{
			name: "exclude path",
			cfg: CustomStabilizerConfigOneOf{
				ExcludePath: &ExcludePath{
					Path: "test/path",
				},
			},
			wantType: reflect.TypeOf(&ExcludePath{}),
			wantNil:  false,
		},
		{
			name:    "no config",
			cfg:     CustomStabilizerConfigOneOf{},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.CustomStabilizerConfig()
			if tt.wantNil {
				if got != nil {
					t.Errorf("CustomStabilizerConfig() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("CustomStabilizerConfig() unexpectedly returned nil")
			}
			gotType := reflect.TypeOf(got)
			if gotType != tt.wantType {
				t.Errorf("CustomStabilizerConfig() type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestCustomStabilizers_EndToEnd_Tar(t *testing.T) {
	testCases := []struct {
		name     string
		input    []*TarEntry
		entries  []CustomStabilizerEntry
		expected []*TarEntry
		wantErr  bool
	}{
		{
			name: "replace_pattern",
			input: []*TarEntry{
				{&tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Hello World")},
				{&tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Changed World")},
			},
		},
		{
			name: "exclude_path",
			input: []*TarEntry{
				{&tar.Header{Name: "test/file1.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
				{&tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Path: "test/file1.txt",
						},
					},
					Reason: "exclude specific file",
				},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Hello World")},
				{&tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Hello World")},
			},
		},
		{
			name: "multiple_custom_stabilizers",
			input: []*TarEntry{
				{&tar.Header{Name: "test/file1.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
				{&tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Path: "test/file1.txt",
						},
					},
					Reason: "exclude specific file",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Hello World")},
				{&tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Changed World")},
			},
		},
		{
			name: "replace_pattern_dir",
			input: []*TarEntry{
				{&tar.Header{Name: "test/", Typeflag: tar.TypeDir}, []byte{}},
				{&tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "test/", Typeflag: tar.TypeDir, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte{}},
				{&tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("Changed World")},
			},
		},
		{
			name: "invalid_custom_stabilizer",
			input: []*TarEntry{
				{&tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "[invalid", // Invalid regex
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create input tar file
			var input bytes.Buffer
			{
				tw := tar.NewWriter(&input)
				for _, entry := range tc.input {
					orDie(tw.WriteHeader(entry.Header))
					must(tw.Write(entry.Body))
				}
				tw.Close()
			}
			customStabilizers, err := CreateCustomStabilizers(tc.entries, TarFormat)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("CreateCustomStabilizers() error: %v", err)
			}
			allStabilizers := append(AllTarStabilizers, customStabilizers...)
			var output bytes.Buffer
			tr := tar.NewReader(bytes.NewReader(input.Bytes()))
			err = StabilizeTar(tr, tar.NewWriter(&output), StabilizeOpts{Stabilizers: allStabilizers})
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("StabilizeTar() error: %v", err)
			}
			var gotEntries []*TarEntry
			{
				tr := tar.NewReader(bytes.NewReader(output.Bytes()))
				for {
					header, err := tr.Next()
					if err == io.EOF {
						break
					}
					orDie(err)
					body := must(io.ReadAll(tr))
					gotEntries = append(gotEntries, &TarEntry{
						Header: header,
						Body:   body,
					})
				}
			}
			if len(gotEntries) != len(tc.expected) {
				t.Fatalf("Got %d entries, want %d", len(gotEntries), len(tc.expected))
			}
			if diff := cmp.Diff(gotEntries, tc.expected); diff != "" {
				t.Errorf("Entries mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

func TestCustomStabilizers_EndToEnd_Zip(t *testing.T) {
	testCases := []struct {
		name     string
		input    []ZipEntry
		entries  []CustomStabilizerEntry
		expected []ZipEntry
		wantErr  bool
	}{
		{
			name: "replace_pattern",
			input: []ZipEntry{
				{&zip.FileHeader{Name: "test/file.txt"}, []byte("Hello World")},
				{&zip.FileHeader{Name: "other/file.txt"}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []ZipEntry{
				{&zip.FileHeader{Name: "other/file.txt", Modified: epoch}, []byte("Hello World")},
				{&zip.FileHeader{Name: "test/file.txt", Modified: epoch}, []byte("Changed World")},
			},
		},
		{
			name: "exclude_path",
			input: []ZipEntry{
				{&zip.FileHeader{Name: "test/file1.txt"}, []byte("Hello World")},
				{&zip.FileHeader{Name: "test/file2.txt"}, []byte("Hello World")},
				{&zip.FileHeader{Name: "other/file.txt"}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Path: "test/file1.txt",
						},
					},
					Reason: "exclude specific file",
				},
			},
			expected: []ZipEntry{
				{&zip.FileHeader{Name: "other/file.txt", Modified: epoch}, []byte("Hello World")},
				{&zip.FileHeader{Name: "test/file2.txt", Modified: epoch}, []byte("Hello World")},
			},
		},
		{
			name: "multiple_custom_stabilizers",
			input: []ZipEntry{
				{&zip.FileHeader{Name: "test/file1.txt"}, []byte("Hello World")},
				{&zip.FileHeader{Name: "test/file2.txt"}, []byte("Hello World")},
				{&zip.FileHeader{Name: "other/file.txt"}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Path: "test/file1.txt",
						},
					},
					Reason: "exclude specific file",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []ZipEntry{
				{&zip.FileHeader{Name: "other/file.txt", Modified: epoch}, []byte("Hello World")},
				{&zip.FileHeader{Name: "test/file2.txt", Modified: epoch}, []byte("Changed World")},
			},
		},
		{
			name: "replace_pattern_dir",
			input: []ZipEntry{
				{&zip.FileHeader{Name: "test/"}, []byte{}},
				{&zip.FileHeader{Name: "test/file.txt"}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []ZipEntry{
				{&zip.FileHeader{Name: "test/", Modified: epoch}, []byte{}},
				{&zip.FileHeader{Name: "test/file.txt", Modified: epoch}, []byte("Changed World")},
			},
		},
		{
			name: "invalid_custom_stabilizer",
			input: []ZipEntry{
				{&zip.FileHeader{Name: "test/file.txt"}, []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Path:    "test/*.txt",
							Pattern: "[invalid", // Invalid regex
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create input zip file
			var zipFile bytes.Buffer
			{
				zw := zip.NewWriter(&zipFile)
				for _, entry := range tc.input {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
			}
			customStabilizers, err := CreateCustomStabilizers(tc.entries, ZipFormat)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("CreateCustomStabilizers() error: %v", err)
			}
			allStabilizers := append(AllZipStabilizers, customStabilizers...)
			reader, size := bytes.NewReader(zipFile.Bytes()), int64(zipFile.Len())
			zipReader, err := zip.NewReader(reader, size)
			if err != nil {
				t.Fatalf("Failed to create zip reader: %v", err)
			}
			var output bytes.Buffer
			zipWriter := zip.NewWriter(&output)
			err = StabilizeZip(zipReader, zipWriter, StabilizeOpts{Stabilizers: allStabilizers})
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("StabilizeZip() error: %v", err)
			}
			zipWriter.Close()
			reader, size = bytes.NewReader(output.Bytes()), int64(output.Len())
			stabilizedZip := must(zip.NewReader(reader, size))
			var gotEntries []ZipEntry
			for _, f := range stabilizedZip.File {
				r := must(f.Open())
				body := must(io.ReadAll(r))
				r.Close()
				gotEntries = append(gotEntries, ZipEntry{
					FileHeader: &f.FileHeader,
					Body:       body,
				})
			}
			if len(gotEntries) != len(tc.expected) {
				t.Fatalf("Got %d entries, want %d", len(gotEntries), len(tc.expected))
			}
			for i, got := range gotEntries {
				want := tc.expected[i]
				if got.Name != want.Name {
					t.Errorf("Entry %d name: got %s, want %s", i, got.Name, want.Name)
				}
				if !bytes.Equal(got.Body, want.Body) {
					t.Errorf("Entry %d body: got %s, want %s", i, got.Body, want.Body)
				}
			}
		})
	}
}
