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
