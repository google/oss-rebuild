// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package kmsdsse

import (
	"testing"
)

func TestKeyRegex(t *testing.T) {
	for _, tc := range []struct {
		value  string
		reject bool
	}{
		{
			value: "projects/my-proj/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/1",
		},
		{
			value:  "projects/my-proj/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/",
			reject: true,
		},
		{
			value:  "projects/my-proj/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/1",
			reject: true,
		},
		{
			value:  "https://cloudkms.googleapis.com/v1/projects/my-proj/locations/us-central1/keyRings/my-ring/cryptoKeys/my-key/cryptoKeyVersions/1",
			reject: true,
		},
	} {
		if matched := keyNameRegex.MatchString(tc.value); matched != !tc.reject {
			t.Errorf("Unexpected regex result: got=%t, expected=%t", matched, !tc.reject)
		}
	}
}
