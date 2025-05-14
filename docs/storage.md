# Attestation Storage

OSS Rebuild stores attestations in a simple, standards-based format designed for reliability and ease-of-use.

## Storage Structure

Attestations are organized hierarchically within their storage bucket:

```
gs://{bucket}/{ecosystem}/{package}/{version}/{artifact}/rebuild.intoto.jsonl
```

Some examples of bucket-relative paths:

- `pypi/absl-py/2.0.0/absl_py-2.0.0-py3-none-any.whl/rebuild.intoto.jsonl`
- `npm/lodash/4.17.21/lodash-4.17.21.tgz/rebuild.intoto.jsonl`

Each `.jsonl` file contains multiple attestations including:

1. A [rebuild attestation](./builds/Rebuild@v0.1.md) describing the build process
2. An [artifact equivalence attestation](./builds/ArtifactEquivalence@v0.1.md) verifying the rebuilt content matches upstream

## Access Methods

### Command-Line Interface

The OSS Rebuild CLI is the easiest way to access attestations:

```bash
# Install
go install github.com/google/oss-rebuild/cmd/oss-rebuild@latest

# List available versions for a package
oss-rebuild list pypi absl-py

# Get attestations for a specific version
oss-rebuild get pypi absl-py 2.0.0

# Access specific components
oss-rebuild get pypi absl-py 2.0.0 --output=dockerfile  # Only the Dockerfile
oss-rebuild get pypi absl-py 2.0.0 --output=bundle      # Raw bundle of DSSEs
```

### Direct Storage Access

For advanced use cases or integrations with other tools, the attestations can be accessed directly from Google Cloud Storage.
See the [product documentation](https://cloud.google.com/storage/docs) for specifics on GCS usage.

#### Authentication

By default, the instance storage bucket is public and can be used without GCP authentication.

#### GCS CLI Access

Attestations can be fetched from GCS using the `gsutil` CLI. For example, you can access Google's OSS Rebuild attestations as follows:

```bash
# Using gsutil
gsutil cat gs://google-rebuild-attestations/pypi/absl-py/2.0.0/absl_py-2.0.0-py3-none-any.whl/rebuild.intoto.jsonl
```

## Verification

All attestations are signed using a host-specific KMS key. The `oss-rebuild` CLI verifies attestation signatures by default using a builtin version of the Google instance key.
Further verification options are detailed in the command help:

```bash
oss-rebuild get --help
```

## Further Resources

- [SLSA Provenance Format](https://slsa.dev/provenance/v1.0)
- [DSSE Specification](https://github.com/secure-systems-lab/dsse/blob/master/envelope.md)
