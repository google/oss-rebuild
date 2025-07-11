// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verdicts

const (
	SelectingArtifact            = "selecting artifact"
	GettingStrategy              = "getting strategy"
	ExecutingRebuild             = "executing rebuild"
	CreatingDebugStore           = "creating debug store"
	CreatingRebuildStore         = "creating rebuild store"
	GettingStabilizers           = "getting stabilizers for target"
	CreatingStabilizers          = "creating stabilizers"
	GettingUpstreamURL           = "getting upstream url"
	ComparingArtifacts           = "comparing artifacts"
	ContentMismatch              = "rebuild content mismatch"
	CreatingAttestations         = "creating attestations"
	PublishingBundle             = "publishing bundle"
	CreatingBuildDefRepoReader   = "creating build definition repo reader"
	AccessingBuildDefinition     = "accessing build definition"
	AccessingStrategy            = "accessing strategy"
	FetchingInference            = "fetching inference"
	ReadingStrategy              = "reading strategy"
	CheckingExistingBundle       = "checking existing bundle"
	ExistingBundle               = "conflict with existing attestation bundle"
	UnknownEcosystem             = "unknown ecosystem"
	UnsupportedEcosystem         = "unsupported ecosystem"
	BadServiceRepoURL            = "bad ServiceRepo URL"
	DisallowedFileServiceRepoURL = "disallowed file:// ServiceRepo URL"
	MismatchedVersion            = "mismatched version"
	MismatchedName               = "mismatched name"
	FailedToExtractUpstream      = "Failed to extract upstream"
	CheckoutFailed               = "Checkout failed"

	// Git
	RepoInvalidOrPrivate = "repo invalid or private"
	CloneFailed          = "clone failed"

	// NPM
	UnknownNpmPackFailure = "unknown npm pack failure"
	UnsupportedNPMVersion = "Unsupported NPM version"
	PackageJSONNotFound   = "package.json file not found"

	// Pypi
	FetchingMetadata                 = "fetching metadata"
	LocatingPureWheel                = "locating pure wheel"
	FailedToGetUpstreamGenerator     = "Failed to get upstream generator"
	UnsupportedGenerator             = "unsupported generator"
	FailedToExtractReqsFromPyproject = "Failed to extract reqs from pyproject.toml."

	// Debian
	DebianRequiresArtifact = "debian requires artifact"

	// Crates
	CargoTOMLNotFound = "Cargo.toml file not found"
)
