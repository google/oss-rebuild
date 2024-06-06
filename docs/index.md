# OSS Rebuild

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
[Trust and Rebuilds](./trust.md).

## Related Projects

Check out these related projects contributing to the reproducible builds effort:

- [reproducible-central](https://github.com/jvm-repo-rebuild/reproducible-central):
  Java, Kotlin reproducibility.
- [kpcyrd/rebuilderd](https://github.com/kpcyrd/rebuilderd): Rebuild scheduler
  with support for several distros.

## Disclaimer

This is not an officially supported Google product.
