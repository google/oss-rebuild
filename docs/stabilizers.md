# Stabilization

Stabilization transforms software packages to remove observable changes due to
the build environment while preserving the software's semantics. This is the
foundation of how OSS Rebuild asserts that two packages are equivalent.

## The Equivalence Philosophy

Reproducible builds aim to verify that a given artifact was built from claimed
source code to reduce the risk of build-time compromise. The ideal is
bit-for-bit identical reproduction but, in practice, many builds produce
different bytes due to benign variations: differing timestamps, file ordering,
build user IDs, and other elements of the build environment. To distinguish this
environmental noise from malicious tampering, stabilizers provide a valuable
abstraction for evaluating artifacts' _functional equivalence_.

Strictly speaking, though, any observable difference in program content, no
matter how trivial, can technically be conditioned upon by an executing system:
A piece of software _could_ check its own creation timestamp and delete the
filesystem if it was built on a Tuesday. It _could_ behave differently based on
whether the builder's UID was 1000 or 1001, or whether gzip level 6 or 9 was
used. However we assert that the required reflection would require such
contrived logic to exploit that we should instead look to detect them by other
means.

By normalizing these low-risk variances, we dramatically increase our
verification coverage (and, consequently, strength) across diverse build
environments without providing meaningful opportunities to attackers.

## Functional Design

At the architectural level, stabilization is implemented as recurrent
application of a series of passes, or "stabilizers," on a single package:

```
stabilization(package) → package
```

Each stabilizer is a pure function, handling a small, focused transformation.
For example, one stabilizer might zeroe timestamps, another normalizes file
ordering, and so on. Like stabilization, each accepts a single package and
outputs a single package:

```
stabilizer(package) → package
```

Asserting equivalence is as simple as asserting

```
equivalence(rebuild, upstream) → stabilization(rebuild) ==
stabilization(upstream)
```

This structural simplicity is intentional and affords a number of benefits:

- **Composable**: Stabilizers can be chained and applied in any order
- **Verifiable**: Stabilizer output can be examined and validated independently
- **Side-effect Free**: No interaction with external state

## Existing Stabilizer Types

Stabilizers normalize metadata across several categories. Each addresses
artifacts that vary by build environment and, especially, by archive format but
don't meaningfully affect the functional behavior of the package.

### TAR

TAR archives contain file metadata including ownership, permissions, and
timestamps that frequently differ between build environments.

- **tar-file-order**: Sorts archive entries alphabetically by filename
- **tar-time**: Sets ModTime and AccessTime to Unix epoch, forces PAX format
- **tar-file-mode**: Sets file mode to 0777 (full permissions)
- **tar-owners**: Zeros Uid, Gid, Uname, and Gname fields
- **tar-xattrs**: Clears extended attributes (Xattrs) and PAX records
- **tar-device-number**: Zeros Devmajor and Devminor fields

### ZIP

ZIP archives contain modification times, version metadata, and filesystem-specific
data that frequently differ from system to system.

- **zip-file-order**: Sorts archive entries alphabetically by filename
- **zip-modified-time**: Zeros Modified, ModifiedDate, and ModifiedTime fields
- **zip-compression**: Sets compression method to Store (no compression)
- **zip-data-descriptor**: Clears data descriptor flag and related size/CRC fields
- **zip-file-encoding**: Sets NonUTF8 flag to false
- **zip-file-mode**: Zeros CreatorVersion and ExternalAttrs fields
- **zip-misc**: Clears Comment, ReaderVersion, Extra, and other miscellaneous flags

### GZIP

GZIP compression adds non-reproducible differences due to configurations such as
compression level or gzip version.

- **gzip-compression**: Sets compression level to NoCompression
- **gzip-name**: Clears the Name field
- **gzip-time**: Sets ModTime to zero value
- **gzip-misc**: Clears Comment and Extra fields, sets OS to 255 (unknown)

### JAR (Java)

Java build tools embed extensive build environment metadata in JAR manifest files
and related properties files. These stabilizers are applied in addition to the
ZIP stabilizers.

- **jar-build-metadata**: Removes 60+ build-related MANIFEST.MF attributes including
  Build-Jdk, Build-Jdk-Spec, Built-By, Build-Time, Build-Date, Build-Number,
  Build-Id, Build-Job, Build-Host, Build-OS, SCM-related fields, and many others
- **jar-attribute-value-order**: Sorts comma-separated values within specific
  manifest attributes (Export-Package, Include-Resource, Provide-Capability,
  Private-Package) to normalize ordering
- **jar-git-properties**: Empties git.json and git.properties files that contain
  VCS commit information

### Crate (Rust)

Rust crates (distributed as .tar.gz files) include version control metadata that
ties the package to a specific git checkout. These stabilizers are applied in
addition to the TAR and GZIP stabilizers.

- **cargo-vcs-hash**: Replaces the git SHA1 hash in `.cargo_vcs_info.json` with
  a placeholder value to remove the dependency on the specific commit hash

### Custom Stabilizers

Sometimes a package has additional sources of non-determinism specific to its
build process. For these cases, custom stabilizers can be defined in its build
definition (which itself must receive 2-party review to be used). The currently
supported custom stabilizer types are:

- **ReplacePattern**: Apply regex replacements to specific files
- **ExcludePath**: Remove files from the package (used mainly for excluding
  accidentally files like test coverage from upstream packages)

Custom stabilizers require a documented reason explaining why the standard
stabilizers are insufficient. This ensures exceptions are deliberate and
auditable.

## Example

Consider a simple tarball containing two files. Here's what stabilization changes:

**Before stabilization:**

```
-rw-r--r-- jenkins/ci  1024 2024-03-15 14:32 src/main.py
-rw-r--r-- jenkins/ci   512 2024-03-15 14:30 lib/utils.py
```

This archive's hash is unique to jenkins, ci, and 2024-03-15. Rebuild it tomorrow and the hash changes.

**After stabilization:**

```
-rwxrwxrwx 0/0           512 1970-01-01 00:00 lib/utils.py
-rwxrwxrwx 0/0          1024 1970-01-01 00:00 src/main.py
```

The stabilizers have:

- Sorted the files alphabetically (lib/utils.py before src/main.py)
- Reset owners to root/0
- Reset timestamps to the epoch
- Normalized permissions

The file contents are unchanged. The resulting archive hash is now purely a function of the file contents.

## Running Stabilizers

The `stabilize` command-line tool provides direct access:

```bash
# Apply all default stabilizers
stabilize -infile package.tar.gz -outfile stabilized.tar.gz

# Disable specific passes
stabilize -infile lib.jar -outfile out.jar \
  -disable-passes="jar-git-properties"
```

See the [stabilize command documentation](../cmd/stabilize/README.md) for full usage details.

## Takeaway

Stabilizers embody a pragmatic security philosophy: perfect is the enemy of the
good. By defining equivalence in terms of functional behavior rather than
byte-for-byte identity, we can verify vastly more packages while maintaining
meaningful security guarantees.
