// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseManifest(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMain  map[string]string
		wantEntry []map[string]string
		wantErr   bool
	}{
		{
			name: "basic manifest",
			input: "Manifest-Version: 1.0\r\n" +
				"Created-By: 1.8.0_45-b14 (Oracle Corporation)\r\n" +
				"\r\n",
			wantMain: map[string]string{
				"Manifest-Version": "1.0",
				"Created-By":       "1.8.0_45-b14 (Oracle Corporation)",
			},
		},
		{
			name: "manifest with entry",
			input: "Manifest-Version: 1.0\r\n" +
				"\r\n" +
				"Name: com/example/test.class\r\n" +
				"SHA-256-Digest: ABCDEF1234567890\r\n" +
				"\r\n",
			wantMain: map[string]string{
				"Manifest-Version": "1.0",
			},
			wantEntry: []map[string]string{{
				"Name":           "com/example/test.class",
				"SHA-256-Digest": "ABCDEF1234567890",
			}},
		},
		{
			name: "manifest with continuation",
			input: "Manifest-Version: 1.0\r\n" +
				"Very-Long-Name: This is a very long value that should be \r\n" +
				" continued on the next line\r\n" +
				"\r\n",
			wantMain: map[string]string{
				"Manifest-Version": "1.0",
				"Very-Long-Name":   "This is a very long value that should be continued on the next line",
			},
		},
		{
			name: "manifest with multiple entries",
			input: "Manifest-Version: 1.0\r\n" +
				"\r\n" +
				"Name: file1.class\r\n" +
				"SHA-256-Digest: ABC\r\n" +
				"\r\n" +
				"Name: file2.class\r\n" +
				"SHA-256-Digest: DEF\r\n" +
				"\r\n",
			wantMain: map[string]string{
				"Manifest-Version": "1.0",
			},
			wantEntry: []map[string]string{
				{
					"Name":           "file1.class",
					"SHA-256-Digest": "ABC",
				},
				{
					"Name":           "file2.class",
					"SHA-256-Digest": "DEF",
				},
			},
		},
		{
			name: "manifest with different line endings",
			input: "Manifest-Version: 1.0\n" +
				"Created-By: test\r" +
				"Build-Jdk: 11\r\n" +
				"\n",
			wantMain: map[string]string{
				"Manifest-Version": "1.0",
				"Created-By":       "test",
				"Build-Jdk":        "11",
			},
		},
		{
			name: "invalid manifest - no colon",
			input: "Manifest-Version: 1.0\r\n" +
				"Invalid Line\r\n" +
				"\r\n",
			wantErr: true,
		},
		{
			name: "invalid manifest - duplicate attribute",
			input: "Manifest-Version: 1.0\r\n" +
				"Created-By: test1\r\n" +
				"Created-By: test2\r\n" +
				"\r\n",
			wantErr: true,
		},
		{
			name: "invalid manifest - invalid name character",
			input: "Manifest-Version: 1.0\r\n" +
				"Invalid@Name: value\r\n" +
				"\r\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			got, err := ParseManifest(r)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseManifest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if diff := cmp.Diff(got.MainSection.attributes, tt.wantMain); diff != "" {
				t.Errorf("Main section differs: (-got,+want)\n%s", diff)
			}

			var sections []map[string]string
			for _, section := range got.EntrySections {
				sections = append(sections, section.attributes)
			}
			if diff := cmp.Diff(sections, tt.wantEntry); diff != "" {
				t.Errorf("Entry sections differ: (-got,+want)\n%s", diff)
			}
		})
	}
}

func TestParseManifestOrder(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantMainOrder []string
		wantErr       bool
	}{
		{
			name: "main attributes order",
			input: "Manifest-Version: 1.0\r\n" +
				"Created-By: test\r\n" +
				"Built-By: user\r\n" +
				"\r\n",
			wantMainOrder: []string{
				"Manifest-Version",
				"Created-By",
				"Built-By",
			},
		},
		{
			name: "manifest version required first",
			input: "Created-By: test\r\n" +
				"Manifest-Version: 1.0\r\n" +
				"Built-By: user\r\n" +
				"\r\n",
			wantMainOrder: []string{
				"Created-By",
				"Manifest-Version",
				"Built-By",
			},
		},
		{
			name: "with continuation line order",
			input: "Manifest-Version: 1.0\r\n" +
				"Long-Attribute: This is a very long value that should \r\n" +
				" be continued on the next line\r\n" +
				"Short-Attribute: value\r\n" +
				"\r\n",
			wantMainOrder: []string{
				"Manifest-Version",
				"Long-Attribute",
				"Short-Attribute",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			got, err := ParseManifest(r)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseManifest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			// Check main section order
			if len(got.MainSection.Names) != len(tt.wantMainOrder) {
				t.Errorf("Main section order length = %d, want %d",
					len(got.MainSection.Names), len(tt.wantMainOrder))
			}
			for i, name := range tt.wantMainOrder {
				if got.MainSection.Names[i] != name {
					t.Errorf("Main section order[%d] = %s, want %s",
						i, got.MainSection.Names[i], name)
				}
			}
		})
	}
}

func TestWriteManifestOrder(t *testing.T) {
	tests := []struct {
		name     string
		manifest *Manifest
		want     string
	}{
		{
			name: "write ordered attributes",
			manifest: func() *Manifest {
				m := NewManifest()
				m.MainSection.Set("Manifest-Version", "1.0")
				m.MainSection.Set("Created-By", "test")
				m.MainSection.Set("Built-By", "user")
				return m
			}(),
			want: "Manifest-Version: 1.0\r\n" +
				"Created-By: test\r\n" +
				"Built-By: user\r\n" +
				"\r\n",
		},
		{
			name: "write with continuation lines",
			manifest: func() *Manifest {
				m := NewManifest()
				m.MainSection.Set("Manifest-Version", "1.0")
				m.MainSection.Set("Long-Attribute",
					"This is a very long value that should be continued on the next line")
				return m
			}(),
			want: "Manifest-Version: 1.0\r\n" +
				"Long-Attribute: This is a very long value that should be continued on \r\n" +
				" the next line\r\n" +
				"\r\n",
		},
		{
			name: "write with continuation lines without spaces",
			manifest: func() *Manifest {
				m := NewManifest()
				m.MainSection.Set("Manifest-Version", "1.0")
				m.MainSection.Set("Long-Attribute", "200aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
				return m
			}(),
			want: "Manifest-Version: 1.0\r\n" +
				"Long-Attribute: 200aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n" +
				" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n" +
				" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n" +
				" aaaaa\r\n" +
				"\r\n",
		},
		{
			name: "write multiple sections",
			manifest: func() *Manifest {
				m := NewManifest()
				m.MainSection.Set("Manifest-Version", "1.0")

				section := NewSection()
				section.Set("Name", "test.class")
				section.Set("SHA-256-Digest", "ABC")
				m.EntrySections = append(m.EntrySections, section)
				return m
			}(),
			want: "Manifest-Version: 1.0\r\n" +
				"\r\n" +
				"Name: test.class\r\n" +
				"SHA-256-Digest: ABC\r\n" +
				"\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteManifest(&buf, tt.manifest)
			if err != nil {
				t.Errorf("WriteManifest() error = %v", err)
				return
			}

			got := buf.String()
			if got != tt.want {
				t.Errorf("WriteManifest() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}
