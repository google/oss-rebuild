// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TestStableTarFileOrderPreservesDuplicateOrder(t *testing.T) {
	// This arrangement causes an unstable sort to reverse the duplicate entries.
	names := []string{"008", "009", "006", "002", "003", "005", "010", "004", "001", "m", "m", "007", "000"}
	bodies := []string{"B", "A"}
	files := make([]*archive.TarEntry, 0, len(names))
	for _, name := range names {
		body := []byte(name)
		if name == "m" {
			body = []byte(bodies[0])
			bodies = bodies[1:]
		}
		files = append(files, &archive.TarEntry{Header: &tar.Header{Name: name}, Body: body})
	}
	fn := StableTarFileOrder.FnFor(NewContext(archive.TarFormat)).(TarArchiveFn)
	fn(&archive.TarArchive{Files: files})
	var got string
	for _, f := range files {
		if f.Name == "m" {
			got += string(f.Body)
		}
	}
	if got != "BA" {
		t.Fatalf("duplicate body order = %q, want BA", got)
	}
}
