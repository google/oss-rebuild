// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"testing"
	"time"
)

func TestStartConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     startConfig
		wantErr bool
	}{
		{"valid", startConfig{API: "http://x", MachineClass: "standard"}, false},
		{"missing api", startConfig{MachineClass: "standard"}, true},
		{"missing machine class", startConfig{API: "http://x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecConfigValidate(t *testing.T) {
	base := func() execConfig {
		return execConfig{API: "http://x", ScratchID: "s", Cmd: []string{"ls"}, PollInterval: time.Second}
	}
	tests := []struct {
		name    string
		mutate  func(*execConfig)
		wantErr bool
	}{
		{"valid", func(*execConfig) {}, false},
		{"missing api", func(c *execConfig) { c.API = "" }, true},
		{"missing scratch id", func(c *execConfig) { c.ScratchID = "" }, true},
		{"missing cmd", func(c *execConfig) { c.Cmd = nil }, true},
		{"non-positive poll interval", func(c *execConfig) { c.PollInterval = 0 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			if err := cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestKillConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     killConfig
		wantErr bool
	}{
		{"valid", killConfig{API: "http://x", ScratchID: "s"}, false},
		{"missing api", killConfig{ScratchID: "s"}, true},
		{"missing scratch id", killConfig{API: "http://x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseExecArgs(t *testing.T) {
	var cfg execConfig
	args := []string{"bash", "-c", "echo hi"}
	if err := parseExecArgs(&cfg, args); err != nil {
		t.Fatalf("parseExecArgs() error = %v", err)
	}
	if len(cfg.Cmd) != len(args) {
		t.Fatalf("Cmd = %v, want %v", cfg.Cmd, args)
	}
	for i := range args {
		if cfg.Cmd[i] != args[i] {
			t.Errorf("Cmd[%d] = %q, want %q", i, cfg.Cmd[i], args[i])
		}
	}
}

func TestParseGCSURI(t *testing.T) {
	tests := []struct {
		uri     string
		bucket  string
		object  string
		wantErr bool
	}{
		{"gs://bucket/path/to/obj", "bucket", "path/to/obj", false},
		{"gs://bucket/obj", "bucket", "obj", false},
		{"http://not-gcs/x", "", "", true},
		{"gs://bucket-only", "", "", true},
		{"gs://bucket/", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			bucket, object, err := parseGCSURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseGCSURI(%q) error = %v, wantErr %v", tt.uri, err, tt.wantErr)
			}
			if err == nil && (bucket != tt.bucket || object != tt.object) {
				t.Errorf("parseGCSURI(%q) = (%q, %q), want (%q, %q)", tt.uri, bucket, object, tt.bucket, tt.object)
			}
		})
	}
}
