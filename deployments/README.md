## Infrastructure and Deployments

### Requirements

Use any terraform-compatible tool to run the infrastructure actuation:

- _Terraform_: [install guide](https://developer.hashicorp.com/terraform/install)
- _OpenTofu_: [install guide](https://opentofu.org/docs/intro/install)

### Configuration

From a local checkout of this repository, Terraform will handle building all
necessary binaries, container images, and deploying the complete service. To
create or update a deployment, configure your rebuilder using a `tfvars` file
as follows:

```hcl
# foo-rebuilder.tfvars

project = "foo-rebuilder"
host = "foo-corp"
public = true
debug = false
repo = "https://github.com/google/oss-rebuild"
```

However this config is lacking the service and prebuild version identifiers.
The `set_version.sh` script is provided to allow for setting these IDs in the
proper format from a local checkout of the repo:

```
$ # Set both service and prebuild by providing two IDs
$ ./set_version.sh foo-rebuilder.tfvars 1edb4fff 1edb4fff
$ # Set only service by providing one ID
$ ./set_version.sh foo-rebuilder.tfvars 21a7b407
```

Finally, actuate the service:

```
$ terraform apply -var-file=foo-rebuilder.tfvars
```

#### Local changes

To push local changes not found in a remote `repo`, configure the `repo` to be
a local `file://` scheme:

```hcl
repo = "file:///home/user/oss-rebuild"
```

`set_version.sh` can then be used to select the service version to build from.

#### Dashboard

The dashboard is optional and served through an external load balancer with
Identity-Aware Proxy, so it works in projects whose organization policy limits
Cloud Run ingress to load balancers and forbids unauthenticated access:

```hcl
enable_dashboard  = true
dashboard_domain  = "dash.example.com"
dashboard_viewers = ["group:dashboard-viewers@example.com", "user:jane@example.com"]
```

After the first apply, create a DNS A record for `dashboard_domain` pointing
at the `dashboard_address` output. The managed certificate is issued once the
record resolves, which can take up to an hour, and `dashboard_url` then serves
the dashboard to the listed viewers after sign-in. Leave `dashboard_domain`
unset to serve on an sslip.io name derived from the address, which needs no
DNS record.

By default IAP uses a Google-managed OAuth client, which admits only viewers
inside the project's organization. To admit other Google identities, create an
OAuth client in the console under Google Auth Platform with an External
audience and a Web application client, set `dashboard_oauth_client_id` and
`dashboard_oauth_client_secret`, apply, then add the client's redirect URI
`https://iap.googleapis.com/v1/oauth/clientIds/<client id>:handleRedirect`.
The service scales to zero and rehydrates the analytics snapshot on its next
request, so the first page after an idle period takes about a minute.
