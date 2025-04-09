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

Example policy file:

```json
{
  "rules": [
    {
      "type": "URLMatchRule",
      "name": "Allow npm registry",
      "pattern": "https://registry.npmjs.org/*",
      "action": "allow"
    },
    {
      "type": "URLMatchRule",
      "name": "Default deny",
      "pattern": "*",
      "action": "deny"
    }
  ]
}
```

## Integration with OSS Rebuild

Within OSS Rebuild, this proxy is configurable to run in the remote rebuild execution. When configured, it captures all network activity during builds and records them for security analysis.

## Limitations

- IPv6 is not currently supported
- Proxy chaining is not supported (this proxy cannot be used behind another proxy)
- Some applications may not honor proxy environment variables or system certificate settings
