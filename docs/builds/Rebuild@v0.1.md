# `Rebuild` Build Type

The Rebuild build type attests to the build process that reproduced the upstream
artifact. It details, among other things, the inputs, build definition,
container definition, and hosted builder steps used to execute the build as well
as the identities of many of the build tools used.

## Attestation Format

### Subject

The `subject` field describes the rebuilt artifact:

| field    | details                                                                                                                               |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `name`   | The file name of the artifact prefixed with `rebuild/`. For many ecosystems this is some combination of the package name and version. |
| `digest` | A hash digest of the artifact, keyed by the algorithm used.                                                                           |

Example:

```
  "subject": [
    {
      "name": "rebuild/absl_py-2.0.0-py3-none-any.whl",
      "digest": {
        "sha256": "bb238e140b6e813c65a8b4be429efbda3ff81fe1b08a5cca0f7b4f316b827ab0"
      }
    }
  ],
```

### External Parameters

The `externalParameters` describe the inputs to the rebuild process. This will
be the upstream artifact, and the rebuild result.

| field                          | details                                                                                                  |
| ------------------------------ | -------------------------------------------------------------------------------------------------------- |
| `ecosystem`                    | The ecosystem identifier associated with the artifact.                                                   |
| `package`                      | The package whose artifact is to be rebuilt.                                                             |
| `version`                      | The package version whose artifact is to be rebuilt.                                                     |
| `artifact`                     | The file name of the artifact to be rebuilt.                                                             |
| `buildConfigSource`            | The location from which the build definition was read. _NOTE: Only for user-generated build definitions_ |
| `buildConfigSource.repository` | The repo URL from which the build definition was read.                                                   |
| `buildConfigSource.ref`        | The repo ref from which the build definition was read.                                                   |
| `buildConfigSource.path`       | The repo relpath from which the build definition was read.                                               |

Example:

```
      "externalParameters": {
        "artifact": "absl_py-2.0.0-py3-none-any.whl",
        "ecosystem": "pypi",
        "package": "absl-py",
        "version": "2.0.0"
        "buildConfigSource": {
          "repository": "https://github.com/google/oss-rebuild",
          "ref": "feedface00000000000000000000000000000000",
          "path": "definitions/pypi/absl-py/2.0.0"
        }
      }
```

### Resolved Dependencies

The `resolvedDependencies` provide the resource identifiers used in the build.
The current dependencies are:

- The package's source repository
- The builder containers
- The input build definition (_NOTE: Only for user-generated build definitions_)

| field     | details                                                                      |
| --------- | ---------------------------------------------------------------------------- |
| `name`    | The source repo and container URLs.                                          |
| `digest`  | When provided, the hash digest of the artifact, keyed by the algorithm used. |
| `content` | When provided, the base64-encoded content of the artifact.                   |

Example:

```
      "resolvedDependencies": [
        {
          "digest": {
            "sha1": "37dad4d356ca9e13f1c533ad6309631b397a2b6b"
          },
          "name": "git+https://github.com/abseil/abseil-py"
        },
        {
          "digest": {
            "sha256": "sha256:9d37de9af1bec96c09bc3e86fa6388d3eccf468370813602d03c7d9ed72a26f8"
          },
          "name": "gcr.io/cloud-builders/gsutil"
        },
        {
          "digest": {
            "sha256": "sha256:0c526a10e09c2690fb451ed7ab27afc15b482d5bf21395de16c8dbd212446a84"
          },
          "name": "gcr.io/cloud-builders/docker"
        }
        {
          "content": "eyJ<snip...>uNy4yIl19fQ==",
          "name": "build.fix.json"
        }
      ]

```

### Byproducts

The `byproducts` include the full file constructs used produce the artifact
such as the high-level definition, the Cloud Build definition, and the specific Dockerfile.

| field     | details                                                  |
| --------- | -------------------------------------------------------- |
| `name`    | The resource identifier for the build process byproduct. |
| `content` | The base64-encoded content of the artifact.              |

Example:

(Content abbreviated for legibility)

```
      "byproducts": [
        {
          "name": "build.json",
          "content": "eyJ<snip...>uNy4yIl19fQ=="
        },
        {
          "name": "Dockerfile",
          "content": "I3N<snip...>GQiXQo="
        },
        {
          "name": "steps.json",
          "content": "W3s<snip...>wWiJ9fV0="
        }
      ]

```

### Internal Parameters

The `internalParameters` provide deployment-specific configuration and source metadata used by the rebuild service:

| field                       | details                                                                 |
| --------------------------- | ----------------------------------------------------------------------- |
| `serviceSource`             | Source metadata for the rebuild service code.                           |
| `serviceSource.repository`  | The repository URL for the rebuild service source code.                 |
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
