// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"os"
)

// RebuildDepsConfig holds the configuration needed to run a rebuild orchestrator.
type RebuildDepsConfig struct {
	BuildProject        string `json:"build_project"`
	AssetBucket         string `json:"asset_bucket"`
	DebugStorage        string `json:"debug_storage"`
	LogsBucket          string `json:"logs_bucket"`
	AttestationBucket   string `json:"attestation_bucket"`
	BuildRemoteIdentity string `json:"build_remote_identity"`

	FirestoreProject           string `json:"firestore_project"`
	InferenceURL               string `json:"inference_url"`
	SigningKeyVersion          string `json:"signing_key_version"`
	PublishForLocalServiceRepo bool   `json:"publish_for_local_service_repo"`
	OverwriteAttestations      bool   `json:"overwrite_attestations"` // TODO: deprecate and remove

	PrebuildRepo   string `json:"prebuild_repo"`
	PrebuildRef    string `json:"prebuild_ref"`
	PrebuildAuth   bool   `json:"prebuild_auth"`
	PrebuildBucket string `json:"prebuild_bucket"`

	BuildDefRepo string `json:"build_def_repo"`
	BuildDefRef  string `json:"build_def_ref"`
	BuildDefDir  string `json:"build_def_dir"`

	GCBPrivatePoolName   string `json:"gcb_private_pool_name"`
	GCBPrivatePoolRegion string `json:"gcb_private_pool_region"`
}

// EnvVar is a simple key-value pair for environment variables.
type EnvVar struct {
	Name  string
	Value string
}

// ToEnv serializes the config to a list of EnvVar.
func (c RebuildDepsConfig) ToEnv() ([]EnvVar, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return []EnvVar{
		{Name: "REBUILD_DEPS_CONFIG", Value: string(b)},
	}, nil
}

// RebuildDepsConfigFromEnv deserializes the config from the environment.
func RebuildDepsConfigFromEnv() (*RebuildDepsConfig, error) {
	val := os.Getenv("REBUILD_DEPS_CONFIG")
	if val == "" {
		return nil, nil
	}
	var c RebuildDepsConfig
	if err := json.Unmarshal([]byte(val), &c); err != nil {
		return nil, err
	}
	return &c, nil
}
