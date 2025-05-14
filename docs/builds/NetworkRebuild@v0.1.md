# `NetworkRebuild` Build Type

The NetworkRebuild build type attests to a rebuild of an upstream that performed
network analysis on the build process itself. It captures network activity and
traffic patterns during the rebuild execution, providing insights into external
dependencies and potential security concerns. This attestation largely mirrors
the [Rebuild Attestation](./Rebuild@v0.1.md) but is oriented solely towards
providing deeper detail on the build's network behavior.

## Attestation Format

### Subject

The `subject` field describes the artifact that was rebuilt while network traffic was monitored:

| field    | details                                                                                                                       |
| -------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `name`   | The file name of the artifact that was rebuilt. For many ecosystems this is some combination of the package name and version. |
| `digest` | A hash digest of the artifact, keyed by the algorithm used.                                                                   |

Example:

```
  "subject": [
    {
      "name": "absl_py-2.0.0-py3-none-any.whl",
      "digest": {
        "sha256": "bb238e140b6e813c65a8b4be429efbda3ff81fe1b08a5cca0f7b4f316b827ab0"
      }
    }
  ],
```

### External Parameters

The `externalParameters` describe the inputs to the network rebuild analysis process:

| field       | details                                                         |
| ----------- | --------------------------------------------------------------- |
| `ecosystem` | The ecosystem identifier associated with the artifact.          |
| `package`   | The package whose artifact was rebuilt with network monitoring. |
| `version`   | The package version whose artifact was rebuilt.                 |
| `artifact`  | The file name of the artifact that was rebuilt.                 |

Example:

```
      "externalParameters": {
        "artifact": "absl_py-2.0.0-py3-none-any.whl",
        "ecosystem": "pypi",
        "package": "absl-py",
        "version": "2.0.0"
      }
```

### Resolved Dependencies

The `resolvedDependencies` provide the resource identifiers used in the network analysis build:

- The source repository used in the rebuild
- The attestation bundle from the original rebuild

| field                      | details                                                 |
| -------------------------- | ------------------------------------------------------- |
| `source`                   | The source repository information (when available).     |
| `source.name`              | The source repository URL with git+ prefix.             |
| `source.digest`            | Git commit hash and other relevant version identifiers. |
| `attestationBundle`        | Reference to the original rebuild attestation bundle.   |
| `attestationBundle.name`   | The asset type identifier for the attestation bundle.   |
| `attestationBundle.digest` | SHA256 hash of the attestation bundle content.          |

Example:

```
      "resolvedDependencies": {
        "source": {
          "name": "git+https://github.com/abseil/abseil-py",
          "digest": {
            "sha1": "37dad4d356ca9e13f1c533ad6309631b397a2b6b"
          }
        },
        "attestationBundle": {
          "name": "rebuild.intoto.jsonl",
          "digest": {
            "sha256": "9d37de9af1bec96c09bc3e86fa6388d3eccf468370813602d03c7d9ed72a26f8"
          }
        }
      }
```

### Byproducts

The `byproducts` include artifacts produced during the network analysis:

| field                   | details                                          |
| ----------------------- | ------------------------------------------------ |
| `networkLog`            | Network traffic log captured during rebuild.     |
| `networkLog.name`       | The URL or identifier for the network log asset. |
| `networkLog.digest`     | SHA256 hash of the network log content.          |
| `buildStrategy`         | The build strategy used for the rebuild.         |
| `buildStrategy.name`    | The resource identifier "build.strategy".        |
| `buildStrategy.content` | The base64-encoded build strategy definition.    |

Example:

```
      "byproducts": {
        "networkLog": {
          "name": "gs://some-analysis-bucket/pypi/absl-py/2.0.0/absl_py-2.0.0-py3-none-any.whl/network/netlog.json",
          "digest": {
            "sha256": "1a2b3c4d5e6f7890abcdef1234567890abcdef1234567890abcdef1234567890"
          }
        },
        "buildStrategy": {
          "name": "build.strategy",
          "content": "eyJ<snip...>uNy4yIl19fQ=="
        }
      }
```

### Internal Parameters

The `internalParameters` provide deployment-specific configuration and source metadata used by the network analyzer service:

| field                       | details                                                                 |
| --------------------------- | ----------------------------------------------------------------------- |
| `serviceSource`             | Source metadata for the network analyzer service code.                  |
| `serviceSource.repository`  | The repository URL for the network analyzer service source code.        |
| `serviceSource.ref`         | The git reference (commit hash, tag, or branch) for the service source. |
| `prebuildSource`            | Source metadata for the prebuild utilities.                             |
| `prebuildSource.repository` | The repository URL for the prebuild utilities source code.              |
| `prebuildSource.ref`        | The git reference for the prebuild utilities source.                    |
| `prebuildConfig`            | Deployment-specific prebuild configuration.                             |
| `prebuildConfig.bucket`     | The Google Cloud Storage bucket containing prebuild utilities.          |
| `prebuildConfig.dir`        | The directory path within the bucket for prebuild utilities (optional). |

Example:

```
      "internalParameters": {
        "prebuildConfig": {
          "bucket": "test-bucket",
          "dir": "test-dir"
        },
        "prebuildSource": {
          "ref": "v0.0.0-202401010000-feeddeadbeef99",
          "repository": "https://github.com/google/oss-rebuild"
        },
        "serviceSource": {
          "ref": "v0.0.0-202501010000-feeddeadbeef00",
          "repository": "https://github.com/google/oss-rebuild"
        }
      }
```
