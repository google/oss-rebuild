// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestExtractTarCommitID(t *testing.T) {
	// Helper to create a tar archive with PAX records
	makeTarWithPAX := func(paxRecords map[string]string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		hdr := &tar.Header{
			Name:       "test-file.txt",
			Size:       4,
			Mode:       0644,
			ModTime:    time.Unix(0, 0),
			PAXRecords: paxRecords,
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("test")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}

		return buf.Bytes()
	}

	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{
			name: "Valid commit ID in PAX records",
			input: makeTarWithPAX(map[string]string{
				"comment": "24a727e23e4143bcc4e5dfac536bae8d8261d32a",
			}),
			want:    "24a727e23e4143bcc4e5dfac536bae8d8261d32a",
			wantErr: false,
		},
		{
			name: "Different valid commit ID",
			input: makeTarWithPAX(map[string]string{
				"comment": "abcdef0123456789abcdef0123456789abcdef01",
			}),
			want:    "abcdef0123456789abcdef0123456789abcdef01",
			wantErr: false,
		},
		{
			name: "Multiple PAX records with commit",
			input: makeTarWithPAX(map[string]string{
				"mtime":   "1234567890.123456789",
				"comment": "24a727e23e4143bcc4e5dfac536bae8d8261d32a",
			}),
			want:    "24a727e23e4143bcc4e5dfac536bae8d8261d32a",
			wantErr: false,
		},
		{
			name:    "No PAX records",
			input:   makeTarWithPAX(nil),
			want:    "",
			wantErr: false,
		},
		{
			name: "PAX records without comment field",
			input: makeTarWithPAX(map[string]string{
				"mtime": "1234567890.123456789",
			}),
			want:    "",
			wantErr: false,
		},
		{
			name: "Partial commit ID (invalid)",
			input: makeTarWithPAX(map[string]string{
				"comment": "24a727e23e4143bcc4e5dfac536",
			}),
			want:    "",
			wantErr: false,
		},
		{
			name: "Commit ID with uppercase (invalid)",
			input: makeTarWithPAX(map[string]string{
				"comment": "24A727E23E4143BCC4E5DFAC536BAE8D8261D32A",
			}),
			want:    "",
			wantErr: false,
		},
		{
			name: "Commit ID with non-hex characters (invalid)",
			input: makeTarWithPAX(map[string]string{
				"comment": "gggggggggggggggggggggggggggggggggggggggg",
			}),
			want:    "",
			wantErr: false,
		},
		{
			name:    "Empty tar",
			input:   []byte{},
			want:    "",
			wantErr: false,
		},
		{
			name:    "Truncated tar",
			input:   []byte{1, 2, 3},
			want:    "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.input)
			got, err := ExtractTarCommitID(r)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractTarCommitID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractTarCommitID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractTarCommitIDWithRealGitArchive(t *testing.T) {
	// Simulate what git-archive actually produces: a tar with global PAX header
	// that gets hoisted into each entry's PAXRecords
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	content := "# Test Repo\n"
	// First entry with the commit ID in PAX records (as git-archive does)
	hdr := &tar.Header{
		Name:    "repo-1.0.0/README.md",
		Size:    int64(len(content)),
		Mode:    0644,
		ModTime: time.Unix(1234567890, 0),
		PAXRecords: map[string]string{
			"comment": "1234567890abcdef1234567890abcdef12345678",
		},
	}

	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	commitID, err := ExtractTarCommitID(&buf)
	if err != nil {
		t.Fatalf("ExtractTarCommitID() error = %v", err)
	}

	want := "1234567890abcdef1234567890abcdef12345678"
	if commitID != want {
		t.Errorf("ExtractTarCommitID() = %v, want %v", commitID, want)
	}
}
