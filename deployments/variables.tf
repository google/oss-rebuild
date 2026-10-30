# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

variable "project" {
  type        = string
  description = "Google Cloud project ID to be used to deploy resources"
}
variable "host" {
  type        = string
  description = "Name of the host of the rebuild instance"
  validation {
    condition     = can(regex("^[a-z][a-z-]*[a-z]$", var.host))
    error_message = "The resource name must start with a letter and contain only lowercase letters and hyphens."
  }
}
variable "repo" {
  type        = string
  description = "Repository URI to be resolved for building and deploying services"
  default     = "https://github.com/google/oss-rebuild"
  validation {
    condition = can(regex((
      # v--------- scheme ----------vv----------------- host -----------------------vv-- port --vv-- path -->
      "^(?:[a-zA-Z][a-zA-Z0-9+.-]*:)?(?:[a-zA-Z0-9-._~!$&'()*+,;=]+|%[0-9a-fA-F]{2})*(?::[0-9]+)?(?:/(?:[a-zA-Z0-9-._~!$&'()*+,;=:@]+|%[0-9a-fA-F]{2})*)*$"
    ), var.repo))
    error_message = "The repo must be a valid URI."
  }
  validation {
    condition = can(startswith(var.repo, "file://")
      ? fileexists(join("/", [substr(var.repo, 7, -1), ".git/config"])) || fileexists(join("/", [substr(var.repo, 7, -1), ".jj/repo/store/git/config"]))
    : true)
    error_message = "file:// URIs must point to valid git repos"
  }
}
variable "service_version" {
  type        = string
  description = "Version identifier to be resolved for building and deploying services. Format must conform to go mod pseudo-version: https://go.dev/ref/mod#pseudo-versions"
  validation {
    condition     = can(regex("^v0.0.0-[0-9]{14}-[0-9a-f]{12}$", var.service_version))
    error_message = "The version must be valid a go mod pseudo-version: https://go.dev/ref/mod#pseudo-versions"
  }
  // TODO: Validate that this is a valid pseudo-version (for external repos).
}
variable "service_commit" {
  type        = string
  description = "Version commit hash to be resolved for building and deploying services. Format must conform to a git full commit hash."
  validation {
    condition     = can(regex("^([0-9a-f]{40}|[0-9a-f]{64})$", var.service_commit))
    error_message = "The commit must be a valid git commit hash"
  }
  validation {
    condition     = substr(var.service_commit, 0, 12) == substr(var.service_version, 22, 12)
    error_message = "The commit must correspond to service_version"
  }
  // TODO: Validate that this commit exists in repo.
}
variable "prebuild_version" {
  type        = string
  description = "Version identifier to be resolved for building and deploying prebuild resources. Format must conform to go mod pseudo-version: https://go.dev/ref/mod#pseudo-versions"
  validation {
    condition     = can(regex("^v0.0.0-[0-9]{14}-[0-9a-f]{12}$", var.prebuild_version))
    error_message = "The version must be valid a go mod pseudo-version: https://go.dev/ref/mod#pseudo-versions"
  }
  // TODO: Validate that this is a valid pseudo-version (for external repos).
}
variable "prebuild_commit" {
  type        = string
  description = "Version commit hash to be resolved for building and deploying prebuild resources. Format must conform to a git full commit hash."
  validation {
    condition     = can(regex("^([0-9a-f]{40}|[0-9a-f]{64})$", var.prebuild_commit))
    error_message = "The commit must be a valid git commit hash"
  }
  validation {
    condition     = substr(var.prebuild_commit, 0, 12) == substr(var.prebuild_version, 22, 12)
    error_message = "The commit must correspond to prebuild_version"
  }
  // TODO: Validate that this commit exists in repo.
}
variable "firestore_location" {
  type        = string
  description = "Location of the default Firestore database."
  default     = "nam5"
}
variable "public" {
  type        = bool
  description = "Whether to enable public access to certain resources like attestations and prebuild resources."
  default     = true
}
variable "debug" {
  type        = bool
  description = "Whether to build and deploy services from debug builds."
  default     = false
}
variable "enable_network_analyzer" {
  type        = bool
  description = "Whether to deploy the network analyzer service"
  default     = false
}
variable "enable_system_analyzer" {
  type        = bool
  description = "Whether to deploy the system analyzer service"
  default     = false
}
variable "enable_private_build_pool" {
  type        = bool
  description = "Whether to create and use a private Cloud Build worker pool for rebuilds"
  default     = false
}
variable "enable_private_pool_peering" {
  type        = bool
  description = "Whether to peer the private build pools into the VPC, for builds that must reach private addresses in it."
  default     = false
  validation {
    condition     = !var.enable_private_pool_peering || var.enable_private_build_pool
    error_message = "enable_private_pool_peering requires enable_private_build_pool"
  }
}
variable "enable_scratch" {
  type        = bool
  description = "Whether to deploy scratch VMs for agent-driven iterative builds."
  default     = false
}
variable "agent_api_max_instances" {
  type        = number
  description = "Max agent-api instances. Carries scratch provisioning and all exec traffic, so it needs headroom when many agent sessions run concurrently."
  default     = 30
}
variable "scratch_machine_type" {
  type        = string
  description = "Machine type for scratch hosts. Each host runs a KVM microVM guest of 4 vCPUs per agent session, two guests per four host vCPUs with memory split evenly, so the type must support nested virtualization (Intel N2, C3 and C4 shapes do; E2 and AMD shapes do not)."
  default     = "n2-standard-16"
}
variable "scratch_jumbo_machine_type" {
  type        = string
  description = "Machine type for jumbo-class scratch hosts, under the same constraints as scratch_machine_type. Empty disables the jumbo class. A type from another family draws on that family's CPU quota, which keeps evals off the standard pool."
  default     = ""
}
variable "scratch_disk_gb" {
  type        = number
  description = "Boot/pd-ssd disk size for scratch hosts. Holds the host image, the golden guest images, and every slot's disks. Its size sets the disk's IOPS and throughput, which the slots share."
  default     = 500
}
variable "enable_internal_ingress" {
  type        = bool
  description = "Whether to restrict Cloud Run services to internal ingress, routing calls between them through the VPC."
  default     = false
}
variable "onboard_dispatch_schedule" {
  type        = string
  description = "Cron schedule on which the API runs an onboarding dispatch pass. Empty leaves dispatch to ctl."
  default     = ""
}
variable "onboard_dispatch_params" {
  type        = map(string)
  description = "Form parameters of the scheduled dispatch pass (batch, max_agent, agent_model, period, ...); omitted ones take the API's defaults."
  default     = {}
}
variable "signals_db_uri" {
  type        = string
  description = "URI the onboarding signal database publishes under (gs://bucket/prefix), read at each snapshot rollup. Empty skips signal ingest."
  default     = ""
}
variable "build_def_repo" {
  type        = string
  description = "Repository URI containing rebuild build definitions"
  default     = "https://github.com/google/oss-rebuild"
  validation {
    condition = can(regex((
      # v--------- scheme ----------vv----------------- host -----------------------vv-- port --vv-- path --
      "^(?:[a-zA-Z][a-zA-Z0-9+.-]*:)?(?:[a-zA-Z0-9-._~!$&'()*+,;=]+|%[0-9a-fA-F]{2})*(?::[0-9]+)?(?:/(?:[a-zA-Z0-9-._~!$&'()*+,;=:@]+|%[0-9a-fA-F]{2})*)*$"
    ), var.build_def_repo))
    error_message = "The build_def_repo must be a valid URI."
  }
}
variable "build_def_repo_dir" {
  type        = string
  description = "Directory within build_def_repo containing build definitions"
  default     = "definitions"
}
variable "enable_dashboard" {
  type        = bool
  description = "Whether to deploy the dashboard service. It serves the analytics snapshot and is reachable only through its load balancer."
  default     = false
}
variable "dashboard_domain" {
  type        = string
  description = "Hostname the dashboard is served on, e.g. dash.example.com. Create its DNS A record at the dashboard_address output after the first apply. Empty derives an sslip.io name from the address, which needs no record."
  default     = ""
}
variable "dashboard_viewers" {
  type        = list(string)
  description = "IAM members admitted to the dashboard by IAP, in user:, group:, or domain: form."
  default     = []
}
variable "dashboard_oauth_client_id" {
  type        = string
  description = "OAuth client ID for the dashboard's IAP. Empty uses IAP's Google-managed client, which admits only identities inside the project's organization. Create a Web application client with an External audience in the console to admit other viewers."
  default     = ""
}
variable "dashboard_oauth_client_secret" {
  type        = string
  description = "Secret of dashboard_oauth_client_id."
  default     = ""
  sensitive   = true
}
