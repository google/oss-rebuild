// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// MemAvailableMB reads the host's MemAvailable, in MB, from /proc/meminfo.
func MemAvailableMB() (int, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	kb, err := procKB(b, "MemAvailable")
	return int(kb >> 10), err
}

// LoadAverage reads the host's one-minute load average from /proc/loadavg.
func LoadAverage() (float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, errors.New("empty /proc/loadavg")
	}
	return strconv.ParseFloat(f[0], 64)
}

// procKB returns the value of a "Name:   1234 kB" line of a procfs file.
func procKB(b []byte, name string) (int64, error) {
	for line := range bytes.Lines(b) {
		if rest, ok := bytes.CutPrefix(line, []byte(name+":")); ok {
			if f := bytes.Fields(rest); len(f) > 0 {
				return strconv.ParseInt(string(f[0]), 10, 64)
			}
		}
	}
	return 0, errors.Errorf("no %s line", name)
}
