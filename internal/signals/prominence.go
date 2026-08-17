// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

// ProminenceRecord is one prominence export row. Rating, in [0,1], is a
// model's answer to how well known the package is by that exact name. It
// ranks the leaf applications prevalence cannot.
type ProminenceRecord struct {
	Ecosystem  string  `json:"ecosystem"`
	Package    string  `json:"package"`
	Model      string  `json:"model"`
	Rating     float64 `json:"rating"`
	Prominence float64 `json:"prominence"` // per-ecosystem rank quantile of Rating, in [0,1]
}
