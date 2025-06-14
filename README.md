# OSS Rebuild

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://github.com/google/oss-rebuild/blob/main/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/google/oss-rebuild)](https://goreportcard.com/report/google/oss-rebuild)
[![Go Reference](https://pkg.go.dev/badge/github.com/google/oss-rebuild.svg)](https://pkg.go.dev/github.com/google/oss-rebuild)

<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/google/oss-rebuild/main/site/logo-light.svg">
    <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/google/oss-rebuild/main/site/logo-dark.svg">
    <img alt="OSS Rebuild logo" src="https://raw.githubusercontent.com/google/oss-rebuild/main/site/logo-dark.svg" height="110" width="230">
  </picture>
</div>

Secure open-source package ecosystems by originating, validating, and augmenting
build attestations.

## Overview

OSS Rebuild aims to apply [reproducible build](https://reproducible-builds.org/)
concepts at low-cost and high-scale for open-source package ecosystems.

Rebuilds are derived by analyzing the published metadata and artifacts and are
evaluated against the upstream package versions. When successful, build
attestations are published for the upstream artifacts, verifying the integrity
of the upstream artifact and eliminating many possible sources of compromise.

We currently support the following ecosystems:

- NPM (JavaScript/TypeScript)
- PyPI (Python)
- Crates.io (Rust)

While complete coverage is the aim, only the most popular packages within each
ecosystem are currently rebuilt.

## Usage

The `oss-rebuild` CLI tool provides access to OSS Rebuild data:

```bash
$ go run github.com/google/oss-rebuild/cmd/oss-rebuild@latest --help
$ # Alternatively, install the binary locally.
$ # Just make sure it's on your PATH: https://go.dev/ref/mod#go-install
$ go install github.com/google/oss-rebuild/cmd/oss-rebuild@latest
$ oss-rebuild --help
```

To view the attestation for a given package, use the `get` command:

```bash
$ oss-rebuild get pypi absl-py 2.0.0
```

The default output contains the rebuild's Dockerfile in base64-encoded form. To
view this Dockerfile alone, we provide an option in the `--output` flag:

```bash
$ oss-rebuild get pypi absl-py 2.0.0 --output=dockerfile
```

This can be chained with the `docker` command to execute a rebuild locally:

```bash
$ oss-rebuild get pypi absl-py 2.0.0 --output=dockerfile | docker run $(docker buildx build -q -)
```

While the default `--output=payload` option produces more human-readable
content, the entire signed attestation can be accessed as follows:

```bash
$ oss-rebuild get pypi absl-py 2.0.0 --output=bundle
```

The `list` command can be used to view the versions of a package that have been
rebuilt:

```bash
$ oss-rebuild list pypi absl-py
```

### Usage Requirements

`oss-rebuild` uses a public [Cloud KMS](https://cloud.google.com/kms/docs) key to validate attestation signatures.
Anonymous authentication is not supported so an [ADC credential](https://cloud.google.com/docs/authentication/set-up-adc-local-dev-environment) must be present.

This can be accomplished with:

```bash
$ gcloud init
$ gcloud auth application-default login
```

To disable signature verification and skip the requirement for KMS access use: `--verify=false`.

## Contributing

Join us in building a more secure and reliable open-source ecosystem!

Check out [the contribution guide](./CONTRIBUTING.md) to learn more.

## Purpose

- **Mitigate supply chain attacks**: Detect discrepancies in open-source
  packages, helping to prevent compromises like those of Solarwinds and
  Codecov.
- **Scale security standards**: Utilize industry best practices such as SLSA,
  Sigstore, and containerized builds.
- **Community participation**: Create a venue to collectivize effort towards
  securing the open-source supply chain.
- **Enable future innovation**: Derive data to leverage AI-driven rebuilds.

## Security

To better understand the security properties of rebuilds, see
[Trust and Rebuilds](./docs/trust.md).

## Related Projects

Check out these related projects contributing to the reproducible builds effort:

- [reproducible-central](https://github.com/jvm-repo-rebuild/reproducible-central):
  Java, Kotlin reproducibility.
- [kpcyrd/rebuilderd](https://github.com/kpcyrd/rebuilderd): Rebuild scheduler
  with support for several distros.

## Disclaimer

This is not an officially supported Google product.
