// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timing

import (
	"strings"
	"time"

	"github.com/pkg/errors"
)

// ContainerSpan reads a container's run duration from
// `docker inspect -f '{{.State.StartedAt}} {{.State.FinishedAt}}'` output:
// the first line holding two RFC3339Nano daemon clocks.
func ContainerSpan(out []byte) (time.Duration, error) {
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		started, err1 := time.Parse(time.RFC3339Nano, f[0])
		finished, err2 := time.Parse(time.RFC3339Nano, f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		return finished.Sub(started), nil
	}
	return 0, errors.New("no container span in output")
}
