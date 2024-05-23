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

| field       | details                                                |
| ----------- | ------------------------------------------------------ |
| `ecosystem` | The ecosystem identifier associated with the artifact. |
| `package`   | The package whose artifact is to be rebuilt.           |
| `version`   | The package version whose artifact is to be rebuilt.   |
| `artifact`  | The file name of the artifact to be rebuilt.           |

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

The `resolvedDependencies` provide the resource identifiers for the source
repository and build containers used.

| field    | details                                                     |
| -------- | ----------------------------------------------------------- |
| `name`   | The source repo and container URLs.                         |
| `digest` | A hash digest of the artifact, keyed by the algorithm used. |

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
