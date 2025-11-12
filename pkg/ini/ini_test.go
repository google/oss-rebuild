// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ini

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *File
		wantErr bool
	}{
		{
			name: "simple key-value pairs",
			input: `key1 = value1
key2 = value2`,
			want: &File{
				Sections: map[string]*Section{
					"": {
						Name: "",
						Values: map[string]string{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "section with key-value pairs",
			input: `[section1]
key1 = value1
key2 = value2`,
			want: &File{
				Sections: map[string]*Section{
					"section1": {
						Name: "section1",
						Values: map[string]string{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "multiple sections",
			input: `[section1]
key1 = value1

[section2]
key2 = value2`,
			want: &File{
				Sections: map[string]*Section{
					"section1": {
						Name: "section1",
						Values: map[string]string{
							"key1": "value1",
						},
					},
					"section2": {
						Name: "section2",
						Values: map[string]string{
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "multiline values",
			input: `[section]
description = This is a long
    description that spans
    multiple lines`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"description": "This is a long\ndescription that spans\nmultiple lines",
						},
					},
				},
			},
		},
		{
			name: "comments",
			input: `# This is a comment
; This is also a comment
[section]
key1 = value1  # inline comment
key2 = value2  ; another inline comment`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "colon separator",
			input: `[section]
key1: value1
key2: value2`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "empty values",
			input: `[section]
key1 =
key2 = value2`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"key1": "",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "values with special characters",
			input: `[section]
url = https://example.com/path?query=value
email = user@example.com`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"url":   "https://example.com/path?query=value",
							"email": "user@example.com",
						},
					},
				},
			},
		},
		{
			name: "section names with dots",
			input: `[options.extras_require]
dev = pytest`,
			want: &File{
				Sections: map[string]*Section{
					"options.extras_require": {
						Name: "options.extras_require",
						Values: map[string]string{
							"dev": "pytest",
						},
					},
				},
			},
		},
		{
			name: "whitespace handling",
			input: `
[section]

key1 = value1

key2 = value2

`,
			want: &File{
				Sections: map[string]*Section{
					"section": {
						Name: "section",
						Values: map[string]string{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "default section before explicit sections",
			input: `default_key = default_value

[section1]
key1 = value1`,
			want: &File{
				Sections: map[string]*Section{
					"": {
						Name: "",
						Values: map[string]string{
							"default_key": "default_value",
						},
					},
					"section1": {
						Name: "section1",
						Values: map[string]string{
							"key1": "value1",
						},
					},
				},
			},
		},
		{
			name:  "inline comment marker without leading whitespace is kept",
			input: `key = value# more value`,
			want: &File{
				Sections: map[string]*Section{
					"": {
						Name: "",
						Values: map[string]string{
							"key": "value# more value",
						},
					},
				},
			},
		},
		{
			name: "section with separator is key-value (python compat)",
			input: `[key1=value1
`,
			want: &File{
				Sections: map[string]*Section{
					"": {
						Name: "",
						Values: map[string]string{
							"[key1": "value1",
						},
					},
				},
			},
		},
		{
			name:    "unclosed section header",
			input:   `[section`,
			wantErr: true,
		},
		{
			name:    "no separator",
			input:   `key without separator`,
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   `= value`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.input))
			if (err != nil) != tc.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetValue(t *testing.T) {
	input := `default = default_value

[section1]
key1 = value1

[section2]
key2 = value2`

	file, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() failed: %v", err)
	}

	tests := []struct {
		name      string
		section   string
		key       string
		wantValue string
		wantFound bool
	}{
		{
			name:      "value in default section",
			section:   "",
			key:       "default",
			wantValue: "default_value",
			wantFound: true,
		},
		{
			name:      "value in section1",
			section:   "section1",
			key:       "key1",
			wantValue: "value1",
			wantFound: true,
		},
		{
			name:      "value in section2",
			section:   "section2",
			key:       "key2",
			wantValue: "value2",
			wantFound: true,
		},
		{
			name:      "nonexistent key",
			section:   "section1",
			key:       "nonexistent",
			wantValue: "",
			wantFound: false,
		},
		{
			name:      "nonexistent section",
			section:   "nonexistent",
			key:       "key1",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotFound := file.GetValue(tc.section, tc.key)
			if gotValue != tc.wantValue {
				t.Errorf("GetValue() value = %q, want %q", gotValue, tc.wantValue)
			}
			if gotFound != tc.wantFound {
				t.Errorf("GetValue() found = %v, want %v", gotFound, tc.wantFound)
			}
		})
	}
}

func TestParse_PythonSetupCfgExample(t *testing.T) {
	// Representative Python setup.cfg
	input := `[metadata]
name = my-package
version = 1.2.3
author = John Doe
long_description = This is a package that
    does amazing things
    across multiple lines

[options]
packages = find:
python_requires = >=3.6
install_requires =
    numpy>=1.19.0
    scipy>=1.5.0
    pandas>=1.1.0
    matplotlib

[options.extras_require]
dev =
    pytest>=6.0
    black
test =
    pytest>=6.0
    coverage`
	want := &File{
		Sections: map[string]*Section{
			"metadata": {
				Name: "metadata",
				Values: map[string]string{
					"name":             "my-package",
					"version":          "1.2.3",
					"author":           "John Doe",
					"long_description": "This is a package that\ndoes amazing things\nacross multiple lines",
				},
			},
			"options": {
				Name: "options",
				Values: map[string]string{
					"packages":         "find:",
					"python_requires":  ">=3.6",
					"install_requires": "\nnumpy>=1.19.0\nscipy>=1.5.0\npandas>=1.1.0\nmatplotlib",
				},
			},
			"options.extras_require": {
				Name: "options.extras_require",
				Values: map[string]string{
					"dev":  "\npytest>=6.0\nblack",
					"test": "\npytest>=6.0\ncoverage",
				},
			},
		},
	}
	got, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() failed: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
	}
}
