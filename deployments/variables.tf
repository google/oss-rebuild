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
variable "enable_vpc" {
  type        = bool
  description = "Whether to create and use VPC infrastructure for private build pools"
  default     = false
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
