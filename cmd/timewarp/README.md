# Timewarp

Timewarp is a registry-fronting HTTP service that filters returned content by
time. This tool allows you to transparently adjust the data returned to package
manager clients to reflect the state of a registry at a given point in time
(especially useful for reproducing prior builds).

In OSS Rebuild, Timewarp helps mitigate environment and logic differences
resulting from package updates by providing a more accurate historical view of
package registry state.

## Overview

Timewarp acts as a proxy between your package manager and the official registry,
filtering responses to only include packages and versions that existed at the
specified point in time. Currently supported registries:

- NPM (https://registry.npmjs.org/)
- PyPI (https://pypi.org/)

## Installation

```bash
go install github.com/google/oss-rebuild/cmd/timewarp@latest
```

Or clone the repository and build:

```bash
git clone https://github.com/google/oss-rebuild.git
cd oss-rebuild
go build ./cmd/timewarp
```

## Usage

Start the Timewarp server:

```bash
timewarp --port 8081
```

The server will listen on the specified port (default: 8081).

### Using with NPM

Set the NPM registry to point to Timewarp, including the desired timestamp in RFC3339 format:

```bash
npm --registry "http://npm:2024-09-13T10:31:26.370Z@localhost:8081" install express
```

Or set it as an environment variable:

```bash
export npm_config_registry="http://npm:2024-09-13T10:31:26.370Z@localhost:8081"
npm install express
```

### Using with pip/PyPI

Set the PyPI index URL to point to Timewarp, including the desired timestamp in RFC3339 format:

```bash
pip install --index-url "http://pypi:2013-12-23T07:45:10.417Z@localhost:8081/simple" requests
```

Or set it as an environment variable:

```bash
export PIP_INDEX_URL="http://pypi:2013-12-23T07:45:10.417Z@localhost:8081/simple"
pip install requests
```

### Using with curl

You can also use curl to directly query the timewarp service:

```bash
curl -u "npm:2024-09-13T10:31:26.370Z" http://localhost:8081/express | jq | less
```

```bash
curl -u "pypi:2013-12-23T07:45:10.417Z" http://localhost:8081/pypi/requests/json | jq | less
```

## Design

Timewarp uses the HTTP Basic Authentication mechanism to pass both the platform
type and target timestamp. The username field specifies the registry type (`npm`
or `pypi`), and the password field contains the RFC3339 timestamp for the
desired point in time.

When a request comes in, Timewarp:

1. Parses the platform and timestamp from Basic Auth credentials
2. Forwards the request to the appropriate upstream registry
3. For JSON responses, filters out versions published after the specified time
4. Returns the modified response to the client

### Details

- Timewarp maintains no state and doesn't cache responses
- For NPM packages, it:
  - Filters out versions published after the specified time
  - Updates the latest version tag to point to the latest version before the cutoff
  - Updates metadata like repository and description to match the new latest version
- For PyPI packages, it:
  - Filters out versions published after the specified time
  - Removes individual files uploaded after the time cutoff
  - Fetches and merges version-specific data for the new latest version

### Limitations

- Time format must be RFC3339 (e.g., `2023-05-13T10:31:26.370Z`)
- The timestamp passed in the request must be no earlier than January 1, 2000
- Some elements of the registry response may not be possible to rewind
  completely (notably certain PyPI metadata)
- The tool hard-codes the upstream NPM and PyPI registries, so it's not
  currently configurable for alternative registries
- Timewarp doesn't support npm's `application/vnd.npm.install-v1+json` format

## Deployment

Timewarp can be deployed:

- As a standalone service
- As a library within an existing service

Within the OSS Rebuild project, it's used both as a library and as a standalone
CLI from Dockerfile builds.

## Debugging

To see more detailed logs, invoke with `go run -v`.
