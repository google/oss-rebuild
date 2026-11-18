// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestLogsURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bucket  string
		attempt rundex.Rebuild
		want    string
	}{
		{"BuildID", "logs", rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{BuildID: "b1", ObliviousID: "o1"}}, "https://storage.cloud.google.com/logs/log-b1.txt"},
		{"ObliviousID", "logs", rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{ObliviousID: "o1"}}, "https://storage.cloud.google.com/logs/log-o1.txt"},
		{"NoID", "logs", rundex.Rebuild{}, ""},
		{"NoBucket", "", rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{BuildID: "b1"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logsURL(tc.bucket, tc.attempt); got != tc.want {
				t.Errorf("logsURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
