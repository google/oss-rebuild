// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/archive"
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
						Paths:   []string{"test/path"},
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
						Paths: []string{"test/path"},
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
						Paths:   []string{"test/path"},
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
						Paths:   []string{"test/path"},
						Pattern: "pattern",
						Replace: "replace",
					},
					ExcludePath: &ExcludePath{
						Paths: []string{"test/path"},
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
				Paths:   []string{"test/path"},
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
			errMsg:  "no path provided",
		},
		{
			name: "invalid pattern",
			rp: ReplacePattern{
				Paths:   []string{"test/path"},
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
				Paths: []string{"test/path"},
			},
			wantErr: false,
		},
		{
			name: "empty path",
			ep: ExcludePath{
				Paths: []string{},
			},
			wantErr: true,
			errMsg:  "no path provided",
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
		Paths:   []string{"test/path"},
		Pattern: "pattern",
		Replace: "replace",
	}
	tests := []struct {
		name     string
		format   archive.Format
		wantFunc any
		wantName string
	}{
		{
			name:     "tar format",
			format:   archive.TarFormat,
			wantFunc: TarEntryFn(nil),
			wantName: "replace-pattern-test",
		},
		{
			name:     "tgz format",
			format:   archive.TarGzFormat,
			wantFunc: TarEntryFn(nil),
			wantName: "replace-pattern-test",
		},
		{
			name:     "zip format",
			format:   archive.ZipFormat,
			wantFunc: ZipEntryFn(nil),
			wantName: "replace-pattern-test",
		},
		{
			name:     "unsupported format",
			format:   archive.UnknownFormat,
			wantFunc: nil,
			wantName: "replace-pattern-test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizer := rp.Stabilizer("test")
			if stabilizer.Name != tt.wantName {
				t.Errorf("Stabilizer() name = %v, want %v", stabilizer.Name, tt.wantName)
			}
			fn := stabilizer.FnFor(NewContext(tt.format))
			if tt.wantFunc == nil {
				if fn != nil {
					t.Errorf("Stabilizer() impl = %T, want nil", fn)
				}
			} else if reflect.TypeOf(fn) != reflect.TypeOf(tt.wantFunc) {
				t.Errorf("Stabilizer() impl type = %T, want %T", fn, tt.wantFunc)
			}
		})
	}
}

// Test ExcludePath.Stabilizer
func TestExcludePath_Stabilizer(t *testing.T) {
	ep := &ExcludePath{
		Paths: []string{"test/path"},
	}
	tests := []struct {
		name     string
		format   archive.Format
		wantFunc any
		wantName string
		wantErr  bool
	}{
		{
			name:     "tar format",
			format:   archive.TarFormat,
			wantFunc: TarArchiveFn(nil),
			wantName: "exclude-path-test",
		},
		{
			name:     "tgz format",
			format:   archive.TarGzFormat,
			wantFunc: TarArchiveFn(nil),
			wantName: "exclude-path-test",
		},
		{
			name:     "zip format",
			format:   archive.ZipFormat,
			wantFunc: ZipArchiveFn(nil),
			wantName: "exclude-path-test",
		},
		{
			name:     "unsupported format",
			format:   archive.UnknownFormat,
			wantFunc: nil,
			wantName: "exclude-path-test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stabilizer := ep.Stabilizer("test")
			if stabilizer.Name != tt.wantName {
				t.Errorf("Stabilizer() name = %v, want %v", stabilizer.Name, tt.wantName)
			}
			fn := stabilizer.FnFor(NewContext(tt.format))
			if tt.wantFunc == nil {
				if fn != nil {
					t.Errorf("Stabilizer() impl = %T, want nil", fn)
				}
			} else if reflect.TypeOf(fn) != reflect.TypeOf(tt.wantFunc) {
				t.Errorf("Stabilizer() impl type = %T, want %T", fn, tt.wantFunc)
			}
		})
	}
}

func TestCreateCustomStabilizers(t *testing.T) {
	tests := []struct {
		name        string
		entries     []CustomStabilizerEntry
		format      archive.Format
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
							Paths:   []string{"test/path1"},
							Pattern: "pattern1",
							Replace: "replace1",
						},
					},
					Reason: "test reason 1",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/path2"},
						},
					},
					Reason: "test reason 2",
				},
			},
			format:  archive.TarFormat,
			wantLen: 2,
		},
		{
			name: "validation error",
			entries: []CustomStabilizerEntry{
				{
					// Missing reason will cause validation error
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/path"},
							Pattern: "pattern",
							Replace: "replace",
						},
					},
				},
			},
			format:      archive.TarFormat,
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
							Paths:   []string{""},
							Pattern: "pattern",
							Replace: "replace",
						},
					},
					Reason: "test reason",
				},
			},
			format:      archive.TarFormat,
			wantErr:     true,
			errContains: "invalid path",
		},
		{
			name: "stabilizer creation error",
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/path"},
							Pattern: "pattern",
							Replace: "replace",
						},
					},
					Reason: "", // This will cause an error in Stabilizer()
				},
			},
			format:      archive.TarFormat,
			wantErr:     true,
			errContains: "no reason provided",
		},
		{
			name:    "empty configs",
			entries: []CustomStabilizerEntry{},
			format:  archive.TarFormat,
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
				fn := stabilizers[i].FnFor(NewContext(tt.format))
				switch {
				case ent.Config.ReplacePattern != nil:
					switch tt.format {
					case archive.TarFormat, archive.TarGzFormat:
						if _, ok := fn.(TarEntryFn); !ok {
							t.Errorf("Stabilizer at index %d is not a TarEntryFn", i)
						}
					case archive.ZipFormat:
						if _, ok := fn.(ZipEntryFn); !ok {
							t.Errorf("Stabilizer at index %d is not a ZipEntryFn", i)
						}
					}
				case ent.Config.ExcludePath != nil:
					switch tt.format {
					case archive.TarFormat, archive.TarGzFormat:
						if _, ok := fn.(TarArchiveFn); !ok {
							t.Errorf("Stabilizer at index %d is not a TarArchiveFn", i)
						}
					case archive.ZipFormat:
						if _, ok := fn.(ZipArchiveFn); !ok {
							t.Errorf("Stabilizer at index %d is not a ZipArchiveFn", i)
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
					Paths:   []string{"test/path"},
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
					Paths: []string{"test/path"},
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
		input    []*archive.TarEntry
		entries  []CustomStabilizerEntry
		expected []*archive.TarEntry
		wantErr  bool
	}{
		{
			name: "replace_pattern",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Changed World")},
			},
		},
		{
			name: "exclude_path",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/file1.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/file1.txt"},
						},
					},
					Reason: "exclude specific file",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
			},
		},
		{
			name: "exclude_path_glob",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/foo/file1.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/bar/file2.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/**/*.txt"},
						},
					},
					Reason: "exclude all test files",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
			},
		},
		{
			name: "exclude_path_multiglob",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/foo/file1.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/bar/file2.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/foo/**", "test/bar/**"},
						},
					},
					Reason: "exclude all test files",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
			},
		},
		{
			name: "multiple_custom_stabilizers",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/file1.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/file1.txt"},
						},
					},
					Reason: "exclude specific file",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "other/file.txt", Typeflag: tar.TypeReg, Size: 11, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Hello World")},
				{Header: &tar.Header{Name: "test/file2.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Changed World")},
			},
		},
		{
			name: "replace_pattern_dir",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/", Typeflag: tar.TypeDir}, Body: []byte{}},
				{Header: &tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/", Typeflag: tar.TypeDir, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte{}},
				{Header: &tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 13, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("Changed World")},
			},
		},
		{
			name: "invalid_custom_stabilizer",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "test/file.txt", Typeflag: tar.TypeReg, Size: 11}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
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
			customStabilizers, err := CreateCustomStabilizers(tc.entries, archive.TarFormat)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("CreateCustomStabilizers() error: %v", err)
			}
			allStabilizers := append(AllTarStabilizers, customStabilizers...)
			var output bytes.Buffer
			tr := tar.NewReader(bytes.NewReader(input.Bytes()))
			err = StabilizeTar(tr, tar.NewWriter(&output), StabilizeOpts{Stabilizers: allStabilizers}, NewContext(archive.TarFormat))
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("StabilizeTar() error: %v", err)
			}
			var gotEntries []*archive.TarEntry
			{
				tr := tar.NewReader(bytes.NewReader(output.Bytes()))
				for header, err := range iterx.ToSeq2(tr, io.EOF) {
					orDie(err)
					body := must(io.ReadAll(tr))
					gotEntries = append(gotEntries, &archive.TarEntry{
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
		input    []archive.ZipEntry
		entries  []CustomStabilizerEntry
		expected []archive.ZipEntry
		wantErr  bool
	}{
		{
			name: "replace_pattern",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/file.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "other/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "other/file.txt", Modified: epoch}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/file.txt", Modified: epoch}, Body: []byte("Changed World")},
			},
		},
		{
			name: "exclude_path",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/file1.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/file2.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "other/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/file1.txt"},
						},
					},
					Reason: "exclude specific file",
				},
			},
			expected: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "other/file.txt", Modified: epoch}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/file2.txt", Modified: epoch}, Body: []byte("Hello World")},
			},
		},
		{
			name: "exclude_path_glob",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/foo/file1.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/bar/file2.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "other/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/**/*.txt"},
						},
					},
					Reason: "exclude all test files",
				},
			},
			expected: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "other/file.txt", Modified: epoch}, Body: []byte("Hello World")},
			},
		},
		{
			name: "multiple_custom_stabilizers",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/file1.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/file2.txt"}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "other/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ExcludePath: &ExcludePath{
							Paths: []string{"test/file1.txt"},
						},
					},
					Reason: "exclude specific file",
				},
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "other/file.txt", Modified: epoch}, Body: []byte("Hello World")},
				{FileHeader: &zip.FileHeader{Name: "test/file2.txt", Modified: epoch}, Body: []byte("Changed World")},
			},
		},
		{
			name: "replace_pattern_dir",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/"}, Body: []byte{}},
				{FileHeader: &zip.FileHeader{Name: "test/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
							Pattern: "Hello",
							Replace: "Changed",
						},
					},
					Reason: "test replacement",
				},
			},
			expected: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/", Modified: epoch}, Body: []byte{}},
				{FileHeader: &zip.FileHeader{Name: "test/file.txt", Modified: epoch}, Body: []byte("Changed World")},
			},
		},
		{
			name: "invalid_custom_stabilizer",
			input: []archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "test/file.txt"}, Body: []byte("Hello World")},
			},
			entries: []CustomStabilizerEntry{
				{
					Config: CustomStabilizerConfigOneOf{
						ReplacePattern: &ReplacePattern{
							Paths:   []string{"test/*.txt"},
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
			customStabilizers, err := CreateCustomStabilizers(tc.entries, archive.ZipFormat)
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
			err = StabilizeZip(zipReader, zipWriter, StabilizeOpts{Stabilizers: allStabilizers}, NewContext(archive.ZipFormat))
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("StabilizeZip() error: %v", err)
			}
			zipWriter.Close()
			reader, size = bytes.NewReader(output.Bytes()), int64(output.Len())
			stabilizedZip := must(zip.NewReader(reader, size))
			var gotEntries []archive.ZipEntry
			for _, f := range stabilizedZip.File {
				r := must(f.Open())
				body := must(io.ReadAll(r))
				r.Close()
				gotEntries = append(gotEntries, archive.ZipEntry{
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
