// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import "slices"

// AllStabilizers is the complete list of all available stabilizers across all formats.
var AllStabilizers = slices.Concat(AllZipStabilizers, AllTarStabilizers, AllGzipStabilizers, AllJarStabilizers, AllCrateStabilizers, AllPypiStabilizers)
