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

The `byproducts` include a hash digest of the normalized version.

| field     | details                                                                                                   |
| --------- | --------------------------------------------------------------------------------------------------------- |
| `name`    | The high-level build definition, Dockerfile, and Google Cloud Build process that implemented the rebuild. |
| `content` | The base64-encoded content of the artifact.                                                               |

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
