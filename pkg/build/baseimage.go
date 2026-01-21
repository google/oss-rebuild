// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

type BaseImageConfig struct {
	Default    string                       `json:"default"`
	Ecosystems map[rebuild.Ecosystem]string `json:"ecosystems"`
}

func (c BaseImageConfig) SelectFor(input rebuild.Input) string {
	if img, ok := c.Ecosystems[input.Target.Ecosystem]; ok {
		return img
	}
	return c.Default
}

func DefaultBaseImageConfig() BaseImageConfig {
	return BaseImageConfig{
		Default: "docker.io/library/alpine:3.21",
		Ecosystems: map[rebuild.Ecosystem]string{
			rebuild.Debian: "docker.io/library/debian:stable-20251103-slim",
			rebuild.Maven:  "docker.io/library/debian:trixie-20250203-slim",
			rebuild.Go:     "docker.io/library/golang:1.25.4-alpine3.22",
		},
	}
}
