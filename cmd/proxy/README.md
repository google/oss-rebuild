# Proxy

Proxy is a transparent HTTP(S) proxy that intercepts and records network activity. It's primarily used within OSS Rebuild to monitor network interactions during the build process, helping to passively enumerate remote dependencies and to identify suspect build behavior.

## Overview

The proxy provides several key capabilities:

- **Transparent HTTP/HTTPS interception**: Captures all network traffic without requiring client configuration.
- **TLS termination**: Manages TLS connections without breaking encryption.
- **Docker integration**: Can monitor both the build container and any child containers it creates.
- **Network monitoring**: Records network activity and exposes via API for later analysis.
- **Policy enforcement**: Optional rule-based enforcement of network access policies.

## Docker Integration

When using the `-docker_addr` flag, the proxy can intercept and monitor network traffic from Docker containers. This is useful for builds that launch containers as part of their process. The proxy can:

1. Set appropriate environment variables in containers for certificate trust
2. Enforce network isolation by constraining all containers to a specific network
3. Apply TLS interception to containerized applications
4. Recursively proxy Docker socket access from containers (when using `-docker_recursive_proxy`)

## Admin Interface

The proxy provides an admin interface (default: `localhost:3127`) with the following endpoints:

- `/cert`: Get the proxy's CA certificate
  - Options: `?format=jks` for Java KeyStore format
- `/summary`: Get JSON summary of all captured network activity
- `/policy`: Get or update the current policy configuration

## Policy Enforcement

The proxy can enforce network access policies when running with `-policy_mode=enforce`. Policies are defined in a JSON file and can be used to:

- Restrict access to specific hostnames or IP ranges
- Allow or deny specific request patterns
- Limit the scope of external network access during builds

Policies are default-deny: a request is allowed only if it satisfies the rules,
and an empty policy blocks everything. `anyOf` allows a request that matches any
listed rule; `allOf` requires a request to match every listed rule. Each rule
names a registered rule type (`URLMatchRule` is built in) and its parameters.

Example policy file (allow only the npm registry):

```json
{
  "Policy": {
    "anyOf": [
      {
        "ruleType": "URLMatchRule",
        "host": "registry.npmjs.org",
        "matchHostBy": "full",
        "path": "/",
        "matchPathBy": "prefix"
      }
    ]
  }
}
```

`matchHostBy` is `full` or `suffix` (a suffix match is by domain part, so
`registry.npmjs.org` matches `npmjs.org` but `notnpmjs.org` does not; an empty
host with `suffix` matches any host). `matchPathBy` is `full` or `prefix`.

## Integration with OSS Rebuild

Within OSS Rebuild, this proxy is configurable to run in the remote rebuild execution. When configured, it captures all network activity during builds and records them for security analysis.

## Limitations

- IPv6 is not currently supported
- Proxy chaining is not supported (this proxy cannot be used behind another proxy)
- Some applications may not honor proxy environment variables or system certificate settings
- TLS/HTTP interception, and the request-level detail in the activity log, apply
  to CONNECT targets on ports 80 and 443. Connections to other ports are handled
  at the CONNECT (host:port) level: in `enforce` mode they are checked against
  the policy by host and rejected if not permitted, and they are recorded in the
  activity log as `CONNECT` entries — but because such a tunnel is forwarded as
  raw bytes, per-request path enforcement and per-request logging are not
  available for it. Route only ports 80/443 to the proxy, or account for this
  host-level granularity, when relying on it for non-standard ports.
