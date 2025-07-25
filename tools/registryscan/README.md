# `registryscan`

## Overview

The `registryscan` tool identifies the earliest point in the crates.io registry
history where a set of packages (from a Cargo.lock file or individual package)
appear with their specified versions. This is useful for reproducing historical
builds and understanding dependency timelines.

## Usage

The tool accepts input in two formats:

```bash
# Using a Cargo.lock file
go run github.com/google/oss-rebuild/tools/registryscan <path_to_lock_file> <cache_dir>

# Using package@version syntax
go run github.com/google/oss-rebuild/tools/registryscan <package@version> <cache_dir>
```

- `<path_to_lock_file>`: Path to the Cargo.lock file containing the dependencies
- `<package@version>`: A specific package and version (e.g., `serde@1.0.2`). The tool will download the package's Cargo.lock automatically.
- `<cache_dir>`: Directory for caching registry index repositories. The tool manages cloning and updating automatically.

## Example

```bash
# Using a Cargo.lock file
go run github.com/google/oss-rebuild/tools/registryscan/main.go ./my-project/Cargo.lock /tmp/registry-cache

# Using package@version syntax (automatically fetches publish date and Cargo.lock)
go run github.com/google/oss-rebuild/tools/registryscan/main.go serde@1.0.193 /tmp/registry-cache
```

Output includes the earliest commit hash and timestamp where all packages were available:

```
Found commit: abc123def456 at time: 2023-12-15T11:00:00Z
```

## Implementation Details

### Registry Index Structure

The tool transparently searches across two types of crates.io registry indices:

- **Current Index**: Live git repository (`rust-lang/crates.io-index`) updated
  continuously with new package publications
- **Snapshot Indices**: Static historical snapshots from `index.crates.io` taken
  at specific dates, useful for older packages

The tool automatically selects the appropriate repositories based on the target
package's publish date, searching from newest to oldest until the target commit
is found. This approach provides complete historical coverage without requiring
manual repository management.

### Search Algorithm

The tool uses a two-phase linear search strategy:

1. **Day-level Scan**: Starting from the publish date (or current time for
   Cargo.lock files), scans backwards checking one commit per ~24-hour window
   until package availability drops
2. **Commit-level Scan**: Within the identified day, scans backwards through all
   commits to find the exact first commit where all packages appear

For each commit, the tool retrieves the git tree, looks up each package's
registry file path (following crates.io's naming convention), and checks for the
specific version string. Blob hashes are cached to avoid re-reading unchanged
files.

## Limitations

1. **Yanked Packages**: Does not detect if packages have been yanked from the registry
2. **Cargo.lock File Performance**: When using a Cargo.lock file directly (not
   package@version), the tool uses the current time as the search starting
   point, which may be inefficient for older lockfiles

## Use Cases

- Reproduce historical builds by identifying the required registry state
- Configure time-based registry proxies for build environments
- Analyze when specific dependency combinations became available
- Support security auditing by tracking package availability timelines
