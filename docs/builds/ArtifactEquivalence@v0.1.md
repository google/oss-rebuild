# `ArtifactEquivalence` Build Type

The artifact equivalence attestation is a claim that two artifacts are equal
after certain non-security relevant aspects have been stabilized (see
[section below](#artifact-stabilization-details)).

Rebuilding exact bit-for-bit identical copies of upstream artifacts is not
always possible. However, in many cases, the only reason a bit-for-bit match
fails is due to non-security sensitive variations (such as file modification
timestamps). The artifact equivalence attestation addresses these cases where
the rebuild artifact only fails to match due to non-security sensitive
differences.

## Attestation Format

### Subject

The `subject` field describes the upstream artifact against which the rebuilt
artifact is being compared for equivalence:

| field    | details                                                                                                      |
| -------- | ------------------------------------------------------------------------------------------------------------ |
| `name`   | The file name of the artifact. For many ecosystems this is some combination of the package name and version. |
| `digest` | A hash digest of the artifact, keyed by the algorithm used.                                                  |

Example:

```
  "subject": [
    {
      "name": "absl_py-2.0.0-py3-none-any.whl",
      "digest": {
        "sha256": "9a28abb62774ae4e8edbe2dd4c49ffcd45a6a848952a5eccc6a49f3f0fc1e2f3"
      }
    }
  ],
```

### External Parameters

The `externalParameters` describe what inputs the artifact equivalence process
was given. This will be the upstream artifact, and the rebuild result.

| field       | details                                               |
| ----------- | ----------------------------------------------------- |
| `candidate` | An identifier used for the rebuilt artifact.          |
| `target`    | The URL for upstream artifact which will be compared. |

Example:

```
      "externalParameters": {
        "candidate": "rebuild/absl_py-2.0.0-py3-none-any.whl",
        "target": "https://files.pythonhosted.org/packages/01/e4/dc0a1dcc4e74e08d7abedab278c795eef54a224363bb18f5692f416d834f/absl_py-2.0.0-py3-none-any.whl"
      }
```

### Resolved Dependencies

The `resolvedDependencies` provide the hash digests for the artifacts being
compared.

| field    | details                                                     |
| -------- | ----------------------------------------------------------- |
| `name`   | The artifact identifier from `externalParameters`.          |
| `digest` | A hash digest of the artifact, keyed by the algorithm used. |

Example:

```
      "resolvedDependencies": [
        {
          "digest": {
            "sha256": "bb238e140b6e813c65a8b4be429efbda3ff81fe1b08a5cca0f7b4f316b827ab0"
          },
          "name": "rebuild/absl_py-2.0.0-py3-none-any.whl"
        },
        {
          "digest": {
            "sha256": "9a28abb62774ae4e8edbe2dd4c49ffcd45a6a848952a5eccc6a49f3f0fc1e2f3"
          },
          "name": "https://files.pythonhosted.org/packages/01/e4/dc0a1dcc4e74e08d7abedab278c795eef54a224363bb18f5692f416d834f/absl_py-2.0.0-py3-none-any.whl"
        }
      }
```

### Byproducts

The `byproducts` include a hash digest of the stabilized version.

| field    | details                                                     |
| -------- | ----------------------------------------------------------- |
| `name`   | The artifact identifier from `externalParameters`.          |
| `digest` | A hash digest of the artifact, keyed by the algorithm used. |

Example:

```
      "byproducts": [
        {
          "digest": {
            "sha256": "21a8a58d3786c8c63993ca71121b0bccf193ebf6c21f890a3702a055025a4949"
          },
          "name": "normalized/absl_py-2.0.0-py3-none-any.whl"
        }
      ]
```

## Artifact Stabilization Details

To compare the rebuilt artifact and the upstream artifact, OSS Rebuild puts both
artifacts through a stabilization process and compares the results. If the
rebuild was successful, then the result of this process for both upstream and
rebuild should be identical artifacts.

### Zip

[Zip](<https://en.wikipedia.org/wiki/ZIP_(file_format)>) is an archive file
format that supports lossless data compression. Zip archives contain
modification times, zip version metadata, and other filesystem specific data
that frequently differ from system to system. We believe this data does not have a
meaningful security impact for the source-based distribution systems like those
supported by OSS Rebuild. For zip based archives, this is the stabilization
process:

1.  Read all the existing zip entries
1.  Create new zip entries with:
    - Exactly the same file contents
    - Exactly the same file name
    - Set the Modified time to 0
1.  Sort the zip entries by filename
1.  Write the zip entries to a new file

### Tar

[tar](<https://en.wikipedia.org/wiki/Tar_(computing)>) is a utility and
accompanying archive format. Tar itself does not provide compression, frequently
that is done using another compression scheme in combination with tar. Tarballs
contain the file mode, owner and group IDs, and a modification time. These
frequently differ between build environments and we do not believe they have a
meaningful security impact for the source-based distribution systems like those
supported by OSS Rebuild. For tar based archives, this is the stabilization
process:

1.  Read all the existing tar entries
1.  Create new tar entries with:
    - The same entry name
    - The same file contents
    - ModTime and AccessTime as 1985 Oct 26 8:15am UTC (an arbitrary date
      time)
    - Uid and Gid of 0
    - Empty Uname and Gname
    - Mode 0777
1.  Sort the tar entries by filename
1.  Write the entries to a new file

### gzip

The [gzip](https://en.wikipedia.org/wiki/Gzip) compression scheme is frequently
combined with tarballs. The gzip scheme also adds non-reproducible differences,
due to configurations such as compression level or gzip version. However, gzip
archives do not affect metadata about the files contained by the inner tarball.
For this reason, OSS Rebuild will unzip and rezip archives, and relies on the
local implementation being deterministic to enable artifact comparison.
