// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/pkg/errors"
)

// Priority signal exports are JSON Lines at a local path or a gs:// URI,
// resolved to a filesystem and name by Deps.FSFor.

// readJSONL decodes every row of the named file.
func readJSONL[T any](fsys billy.Filesystem, name string) ([]T, error) {
	r, err := fsys.Open(name)
	if err != nil {
		return nil, errors.Wrapf(err, "opening %s", name)
	}
	defer r.Close()
	rows, err := jsonl.DecodeAll[T](r)
	return rows, errors.Wrapf(err, "decoding %s", name)
}

// writeJSONL writes rows to the named file, one line per row.
func writeJSONL[T any](fsys billy.Filesystem, name string, rows []T) error {
	w, err := fsys.Create(name)
	if err != nil {
		return errors.Wrapf(err, "opening %s", name)
	}
	if err := jsonl.Encode(w, rows); err != nil {
		w.Close()
		return err
	}
	return errors.Wrapf(w.Close(), "finalizing %s", name)
}
