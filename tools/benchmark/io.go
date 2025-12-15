// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package benchmark

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

func WriteBenchmark(path string, pset PackageSet) error {
	f, err := os.Create(path)
	if err != nil {
		return errors.Wrap(err, "creating output file")
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pset); err != nil {
		return errors.Wrap(err, "encoding benchmark")
	}
	return nil
}
