# Stabilize

`stabilize` is a command-line tool that removes non-deterministic metadata from software packages to facilitate functional comparison of artifacts.

## Overview

Software artifacts often contain non-deterministic metadata (timestamps, file ordering, compression levels, etc.) that can cause bit-for-bit differences that lack security or correctness implicitions in otherwise identical build results. `stabilize` addresses this problem by applying transformations to archive files to make them consistently comparable.

The tool supports multiple archive formats:

- ZIP (.zip, .whl, .egg, .jar)
- TAR (.tar)
- TAR+GZIP (.tar.gz, .tgz, .crate)

## Installation

```bash
go install github.com/google/oss-rebuild/cmd/stabilize@latest
```

Or build from source:

```bash
git clone https://github.com/google/oss-rebuild
cd oss-rebuild
go build ./cmd/stabilize
```

### Example Usage

#### Basic Usage (Apply All Stabilizers)

```bash
stabilize -infile package-1.0.0.whl -outfile stabilized.whl
```

#### Enable Only Specific Stabilizers

```bash
stabilize -infile package-1.0.0.tgz -outfile stabilized.tgz -enable-passes="tar-file-order,tar-time,tar-owners"
```

#### Disable Specific Stabilizers

```bash
stabilize -infile library-1.0.0.jar -outfile stabilized.jar -disable-passes="jar-git-properties"
```

#### Specify Ecosystem When Ambiguous

```bash
stabilize -infile package-1.0.0.tgz -outfile stabilized.tgz -ecosystem=npm
```

## Available Stabilizers

The tool applies different sets of stabilizers based on the file format. Run `stabilize -help` for a list of all supported stabilizers.
