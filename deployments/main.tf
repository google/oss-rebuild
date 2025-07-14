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

data "google_project" "project" {
  project_id = var.project
}

locals {
  project_num = data.google_project.project.number
}

provider "google" {
  project = var.project
  region  = "us-central1"
  zone    = "us-central1-c"
}

## IAM resources

resource "google_service_account" "orchestrator" {
  account_id  = "orchestrator"
  description = "Primary API identity for the rebuilder. NOTE: This should NOT be used to run rebuilds."
}
resource "google_service_account" "builder-remote" {
  account_id  = "builder-remote"
  description = "Rebuild identity used to run rebuilds executed remotely from the RPC service node."
}
resource "google_service_account" "builder-local" {
  account_id  = "builder-local"
  description = "Rebuild identity used to run rebuilds executed locally on the RPC service node."
}
resource "google_service_account" "inference" {
  account_id  = "inference"
  description = "Identity serving inference-only endpoints."
}
resource "google_service_account" "git-cache" {
  account_id  = "git-cache"
  description = "Identity serving git-cache endpoint."
}
resource "google_service_account" "gateway" {
  account_id  = "gateway"
  description = "Identity serving gateway endpoint."
}
resource "google_service_account" "agent-job" {
  account_id  = "agent-job"
  description = "Identity for AI agent Cloud Run Jobs."
}
resource "google_service_account" "builder-agent" {
  account_id  = "builder-agent"
  description = "Build identity for AI agent rebuilds with oblivious access pattern."
}
resource "google_service_account" "attestation-pubsub-reader" {
  count       = var.public ? 0 : 1
  account_id  = "attestation-pubsub-reader"
  description = "Identity for reading the attestation pubsub topic."
}
resource "google_service_account" "network-analyzer" {
  count       = var.enable_network_analyzer ? 1 : 0
  account_id  = "network-analyzer"
  description = "Primary API identity for the network analyzer"
}
resource "google_service_account" "network-analyzer-build" {
  count       = var.enable_network_analyzer ? 1 : 0
  account_id  = "network-analyzer-build"
  description = "Build identity for the network analyzer"
}
data "google_storage_project_service_account" "attestation-pubsub-publisher" {
}

## KMS resources

resource "google_project_service" "cloudkms" {
  service = "cloudkms.googleapis.com"
}
resource "google_kms_key_ring" "ring" {
  name       = "ring"
  location   = "global"
  depends_on = [google_project_service.cloudkms]
}
resource "google_kms_crypto_key" "signing-key" {
  name     = "signing-key"
  key_ring = google_kms_key_ring.ring.id
  purpose  = "ASYMMETRIC_SIGN"
  version_template {
    algorithm = "EC_SIGN_P256_SHA256"
  }
  lifecycle {
    prevent_destroy = true
  }
}
data "google_kms_crypto_key_version" "signing-key-version" {
  crypto_key = google_kms_crypto_key.signing-key.id
}

## Storage resources

resource "google_project_service" "firestore" {
  service = "firestore.googleapis.com"
}
resource "google_project_service" "storage" {
  service = "storage.googleapis.com"
}
resource "google_storage_bucket" "attestations" {
  name                        = "${var.host}-rebuild-attestations"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
resource "google_storage_bucket" "metadata" {
  name                        = "${var.host}-rebuild-metadata"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
resource "google_storage_bucket" "logs" {
  name                        = "${var.host}-rebuild-logs"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
resource "google_storage_bucket" "debug" {
  name                        = "${var.host}-rebuild-debug"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
resource "google_storage_bucket" "git-cache" {
  name                        = "${var.host}-rebuild-git-cache"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
resource "google_storage_bucket" "bootstrap-tools" {
  name                        = "${var.host}-rebuild-bootstrap-tools"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  # Objects should not be deleted or replaced.
  default_event_based_hold = true
  depends_on               = [google_project_service.storage]
}
resource "google_storage_bucket" "network-analyzer-attestations" {
  count                       = var.enable_network_analyzer ? 1 : 0
  name                        = "${var.host}-network-analyzer-attestations"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
# Stores the list of "tracked" packages that are eligible for automatic rebuilds.
resource "google_storage_bucket" "tracked-packages" {
  name                        = "${var.host}-rebuild-tracked-packages"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
  versioning {
    enabled = true
  }
}

# Agent-related GCS buckets for AI agent feature
resource "google_storage_bucket" "agent-sessions" {
  name                        = "${var.host}-rebuild-agent-sessions"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}

resource "google_storage_bucket" "agent-logs" {
  name                        = "${var.host}-rebuild-agent-logs"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}

resource "google_storage_bucket" "agent-metadata" {
  name                        = "${var.host}-rebuild-agent-metadata"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}

## Firestore

resource "google_project_service" "compute" {
  service = "compute.googleapis.com"
}
resource "google_project_service" "gae" {
  service = "appengine.googleapis.com"
}
# NOTE: Side-effect of app creation is creation of Firestore DB.
resource "google_app_engine_application" "dummy_app" {
  project       = var.project
  location_id   = "us-central"
  database_type = "CLOUD_FIRESTORE"
  depends_on    = [google_project_service.gae]
}

## PubSub

resource "google_pubsub_topic" "attestation-topic" {
  // TODO: Might want to make this ${var.host}-attestation-topic
  name = "oss-rebuild-attestation-topic"
}

resource "google_storage_notification" "attestation-notification" {
  bucket         = google_storage_bucket.attestations.name
  payload_format = "JSON_API_V1"
  topic          = google_pubsub_topic.attestation-topic.id
  event_types    = ["OBJECT_FINALIZE"]
  depends_on     = [google_pubsub_topic_iam_binding.readers-can-read-from-attestation-topic, google_pubsub_topic_iam_binding.can-publish-to-attestation-topic]
}

resource "google_project_service" "cloudtasks" {
  service = "cloudtasks.googleapis.com"
}

resource "google_cloud_tasks_queue" "network-analyzer-queue" {
  count    = var.enable_network_analyzer ? 1 : 0
  name     = "network-analyzer-queue"
  location = "us-central1"
  rate_limits {
    max_concurrent_dispatches = 50
    max_dispatches_per_second = 5
  }
  retry_config {
    max_attempts       = 1
    min_backoff        = "10s"
    max_backoff        = "300s"
    max_retry_duration = "600s"
  }
  depends_on = [google_project_service.cloudtasks]
}

resource "google_pubsub_subscription" "network-analyzer-feed" {
  count = var.enable_network_analyzer ? 1 : 0
  name  = "network-analyzer-feed"
  topic = google_pubsub_topic.attestation-topic.id
  push_config {
    push_endpoint = "${google_cloud_run_v2_service.network-subscriber[0].uri}/enqueue"
    no_wrapper {
      write_metadata = false
    }
    oidc_token {
      service_account_email = google_service_account.network-analyzer[0].email
    }
  }
  message_retention_duration = "${7 * 24 * 60 * 60}s" # 7 days
  ack_deadline_seconds       = 600
}

## Container resources

resource "google_artifact_registry_repository" "registry" {
  location      = "us-central1"
  repository_id = "service-images"
  format        = "DOCKER"
  docker_config {
    immutable_tags = true
  }
}

resource "terraform_data" "service_version" {
  input = var.service_version
}

resource "terraform_data" "prebuild_version" {
  input = var.prebuild_version
}

resource "terraform_data" "debug" {
  input = var.debug
}

locals {
  registry_url = "${google_artifact_registry_repository.registry.location}-docker.pkg.dev/${var.project}/${google_artifact_registry_repository.registry.repository_id}"
  service_images = merge({
    gateway = {
      dockerfile = "build/package/Dockerfile.gateway"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    git_cache = {
      dockerfile = "build/package/Dockerfile.git_cache"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    rebuilder = {
      dockerfile = "build/package/Dockerfile.rebuilder"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    inference = {
      dockerfile = "build/package/Dockerfile.inference"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    api = {
      dockerfile = "build/package/Dockerfile.api"
      build_args = [
        "DEBUG=${terraform_data.debug.output}",
        "BUILD_REPO=${var.repo}",
        "BUILD_VERSION=${terraform_data.service_version.output}"
      ]
    }
    agent-api = {
      dockerfile = "build/package/Dockerfile.agent-api"
      build_args = [
        "DEBUG=${terraform_data.debug.output}",
        "BUILD_REPO=${var.repo}",
        "BUILD_VERSION=${terraform_data.service_version.output}"
      ]
    }
    agent = {
      dockerfile = "build/package/Dockerfile.agent"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    }, var.enable_network_analyzer ? {
    network-analyzer = {
      dockerfile = "build/package/Dockerfile.networkanalyzer"
      build_args = [
        "DEBUG=${terraform_data.debug.output}",
        "BUILD_REPO=${var.repo}",
        "BUILD_VERSION=${terraform_data.service_version.output}"
      ]
    }
    network-subscriber = {
      dockerfile = "build/package/Dockerfile.networksubscriber"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
  } : {})
  prebuild_images = {
    gsutil_writeonly = {
      dockerfile = "build/package/Dockerfile.gsutil_writeonly"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    proxy = {
      dockerfile = "build/package/Dockerfile.proxy"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
    timewarp = {
      dockerfile = "build/package/Dockerfile.timewarp"
      build_args = ["DEBUG=${terraform_data.debug.output}"]
    }
  }
}

module "service_images" {
  source = "./modules/container_build_push"

  for_each = local.service_images

  name            = each.key
  image_url       = "${local.registry_url}/${each.key}"
  image_version   = terraform_data.service_version.output
  repo_url        = var.repo
  commit          = var.service_commit
  dockerfile_path = each.value.dockerfile
  build_args      = each.value.build_args
}

module "prebuild_images" {
  source = "./modules/container_build_push"

  for_each = local.prebuild_images

  name            = each.key
  image_url       = "${local.registry_url}/${each.key}"
  image_version   = terraform_data.prebuild_version.output
  repo_url        = var.repo
  commit          = var.prebuild_commit
  dockerfile_path = each.value.dockerfile
  build_args      = each.value.build_args
}

module "prebuild_binaries" {
  source = "./modules/container_binary_upload"

  for_each = local.prebuild_images

  source_image_url = module.prebuild_images[each.key].full_image_url
  binary_name      = each.key
  gcs_destination  = "gs://${google_storage_bucket.bootstrap-tools.name}/${module.prebuild_images[each.key].image_version}/${each.key}"

  depends_on = [module.prebuild_images]
}

data "google_artifact_registry_docker_image" "gateway" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "gateway:${module.service_images["gateway"].image_version}"
  depends_on    = [module.service_images["gateway"]]
}

data "google_artifact_registry_docker_image" "git-cache" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "git_cache:${module.service_images["git_cache"].image_version}"
  depends_on    = [module.service_images["git_cache"]]
}

data "google_artifact_registry_docker_image" "rebuilder" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "rebuilder:${module.service_images["rebuilder"].image_version}"
  depends_on    = [module.service_images["rebuilder"]]
}

data "google_artifact_registry_docker_image" "inference" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "inference:${module.service_images["inference"].image_version}"
  depends_on    = [module.service_images["inference"]]
}

data "google_artifact_registry_docker_image" "api" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "api:${module.service_images["api"].image_version}"
  depends_on    = [module.service_images["api"]]
}

data "google_artifact_registry_docker_image" "agent-api" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "agent-api:${module.service_images["agent-api"].image_version}"
  depends_on    = [module.service_images["agent-api"]]
}

data "google_artifact_registry_docker_image" "agent" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "agent:${module.service_images["agent"].image_version}"
  depends_on    = [module.service_images["agent"]]
}

data "google_artifact_registry_docker_image" "network-analyzer" {
  count         = var.enable_network_analyzer ? 1 : 0
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "network-analyzer:${module.service_images["network-analyzer"].image_version}"
  depends_on    = [module.service_images["network-analyzer"]]
}

data "google_artifact_registry_docker_image" "network-subscriber" {
  count         = var.enable_network_analyzer ? 1 : 0
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "network-subscriber:${module.service_images["network-subscriber"].image_version}"
  depends_on    = [module.service_images["network-subscriber"]]
}

## Network resources

resource "google_project_service" "servicenetworking" {
  count   = var.enable_vpc ? 1 : 0
  service = "servicenetworking.googleapis.com"
}
resource "google_compute_network" "vpc" {
  count                   = var.enable_vpc ? 1 : 0
  name                    = "${var.host}-rebuild-vpc"
  auto_create_subnetworks = false
}
resource "google_compute_subnetwork" "subnet" {
  count         = var.enable_vpc ? 1 : 0
  name          = "${var.host}-rebuild-subnet"
  ip_cidr_range = "10.10.1.0/24"
  region        = "us-central1"
  network       = google_compute_network.vpc[0].name
}
resource "google_service_networking_connection" "private_service_access" {
  count                   = var.enable_vpc ? 1 : 0
  network                 = google_compute_network.vpc[0].id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_service_access[0].name]
}
# Reserve IP range for Google services to connect to your VPC
resource "google_compute_global_address" "private_service_access" {
  count         = var.enable_vpc ? 1 : 0
  name          = "${var.host}-rebuild-private-service-access"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 20 # 4k IPs
  network       = google_compute_network.vpc[0].id
}
# NAT for outbound internet access from private build pools
resource "google_compute_router" "router" {
  count   = var.enable_vpc ? 1 : 0
  name    = "${var.host}-rebuild-router"
  region  = "us-central1"
  network = google_compute_network.vpc[0].id
}
resource "google_compute_router_nat" "nat" {
  count  = var.enable_vpc ? 1 : 0
  name   = "${var.host}-rebuild-nat"
  router = google_compute_router.router[0].name
  region = "us-central1"
  # Auto-allocate for all IPs
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}
resource "google_compute_firewall" "allow_internal" {
  count   = var.enable_vpc ? 1 : 0
  name    = "${var.host}-rebuild-allow-internal"
  network = google_compute_network.vpc[0].name
  allow {
    protocol = "tcp"
    ports    = ["0-65535"]
  }
  allow {
    protocol = "udp"
    ports    = ["0-65535"]
  }
  source_ranges = ["${google_compute_global_address.private_service_access[0].address}/${google_compute_global_address.private_service_access[0].prefix_length}"]
}
resource "google_compute_firewall" "allow_outbound" {
  count     = var.enable_vpc ? 1 : 0
  name      = "${var.host}-rebuild-allow-outbound"
  network   = google_compute_network.vpc[0].name
  direction = "EGRESS"
  allow {
    protocol = "tcp"
  }
  allow {
    protocol = "udp"
  }
  destination_ranges = ["0.0.0.0/0"]
}

## Compute resources

resource "google_project_service" "cloudbuild" {
  service = "cloudbuild.googleapis.com"
}

resource "google_cloudbuild_worker_pool" "private-pool" {
  count    = var.enable_private_build_pool ? 1 : 0
  name     = "${var.host}-rebuild-pool"
  location = "us-central1"
  worker_config {
    machine_type = "e2-standard-4"
    disk_size_gb = 100
  }
  dynamic "network_config" {
    for_each = var.enable_vpc ? [1] : []
    content {
      peered_network          = google_compute_network.vpc[0].id
      peered_network_ip_range = "/22" # 1k IPs
    }
  }
  depends_on = [google_project_service.cloudbuild]
}

resource "google_project_service" "run" {
  service = "run.googleapis.com"
}
resource "google_cloud_run_v2_service" "gateway" {
  name     = "gateway"
  location = "us-central1"
  template {
    service_account = google_service_account.gateway.email
    timeout         = "${5 * 60}s" // 5 minutes
    containers {
      image = data.google_artifact_registry_docker_image.gateway.self_link
      resources {
        limits = {
          cpu    = "1000m"
          memory = "2G"
        }
      }
    }
    scaling { max_instance_count = 1 }
    # Start to reject requests once a queue is at or near saturation. See cmd/gateway/main.go
    max_instance_request_concurrency = 360
  }
  depends_on = [google_project_service.run]
}
resource "google_cloud_run_v2_service" "git-cache" {
  name     = "git-cache"
  location = "us-central1"
  template {
    service_account = google_service_account.git-cache.email
    timeout         = "${2 * 60}s" // 2 minutes
    containers {
      image = data.google_artifact_registry_docker_image.git-cache.self_link
      args = [
        "--bucket=${google_storage_bucket.git-cache.name}"
      ]
      resources {
        limits = {
          cpu    = "1000m"
          memory = "2G"
        }
      }
    }
    max_instance_request_concurrency = 1
  }
  depends_on = [google_project_service.run]
}
resource "google_cloud_run_v2_service" "build-local" {
  name     = "build-local"
  location = "us-central1"
  template {
    service_account = google_service_account.builder-local.email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.rebuilder.self_link
      args = [
        "--debug-storage=gs://${google_storage_bucket.debug.name}",
        "--git-cache-url=${google_cloud_run_v2_service.git-cache.uri}",
        "--gateway-url=${google_cloud_run_v2_service.gateway.uri}",
        "--user-agent=oss-rebuild+${var.host}/0.0.0",
      ]
      resources {
        limits = {
          cpu    = "8000m"
          memory = "24G"
        }
      }
    }
    scaling { max_instance_count = 100 }
    max_instance_request_concurrency = 1
  }
  depends_on = [google_project_service.run]
}
resource "google_cloud_run_v2_service" "inference" {
  name     = "inference"
  location = "us-central1"
  template {
    service_account = google_service_account.inference.email
    timeout         = "${14 * 60}s" // 14 minutes
    containers {
      image = data.google_artifact_registry_docker_image.inference.self_link
      args = [
        "--gateway-url=${google_cloud_run_v2_service.gateway.uri}",
        "--user-agent=oss-rebuild+${var.host}/0.0.0",
        "--git-cache-url=${google_cloud_run_v2_service.git-cache.uri}",
      ]
      resources {
        limits = {
          cpu    = "4000m"
          memory = "16G"
        }
      }
    }
    max_instance_request_concurrency = 1
  }
  depends_on = [google_project_service.run]
}

resource "google_cloud_run_v2_service" "orchestrator" {
  name     = "api"
  location = "us-central1"
  template {
    service_account = google_service_account.orchestrator.email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.api.self_link
      args = concat([
        "--project=${var.project}",
        "--build-local-url=${google_cloud_run_v2_service.build-local.uri}",
        "--build-remote-identity=${google_service_account.builder-remote.name}",
        "--inference-url=${google_cloud_run_v2_service.inference.uri}",
        "--prebuild-bucket=${google_storage_bucket.bootstrap-tools.name}",
        "--prebuild-version=${var.prebuild_version}",
        "--prebuild-auth=${var.public ? "false" : "true"}",
        "--signing-key-version=${data.google_kms_crypto_key_version.signing-key-version.name}",
        "--metadata-bucket=${google_storage_bucket.metadata.name}",
        "--attestation-bucket=${google_storage_bucket.attestations.name}",
        "--logs-bucket=${google_storage_bucket.logs.name}",
        "--debug-storage=gs://${google_storage_bucket.debug.name}",
        "--gateway-url=${google_cloud_run_v2_service.gateway.uri}",
        "--user-agent=oss-rebuild+${var.host}/0.0.0",
        "--build-def-repo=https://github.com/google/oss-rebuild",
        "--build-def-repo-dir=definitions",
        "--block-local-repo-publish=${var.public}",
        "--agent-job-name=${google_cloud_run_v2_job.agent.id}",
        "--agent-api-url=${google_cloud_run_v2_service.agent-api.uri}",
        "--agent-timeout-seconds=3600", // 1 hour
        "--agent-sessions-bucket=${google_storage_bucket.agent-sessions.name}",
        "--agent-metadata-bucket=${google_storage_bucket.agent-metadata.name}",
        ], var.enable_private_build_pool ? [
        "--gcb-private-pool-name=${google_cloudbuild_worker_pool.private-pool[0].id}",
        "--gcb-private-pool-region=us-central1",
      ] : [])
      resources {
        limits = {
          cpu    = "1000m"
          memory = "2G"
        }
      }
    }
    max_instance_request_concurrency = 25
  }
  depends_on = [google_project_service.run, module.prebuild_binaries]
}
resource "google_cloud_run_v2_service" "network-analyzer" {
  count    = var.enable_network_analyzer ? 1 : 0
  name     = "network-analyzer"
  location = "us-central1"
  template {
    service_account = google_service_account.network-analyzer[0].email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.network-analyzer[0].self_link
      args = concat([
        "--project=${var.project}",
        "--analysis-bucket=${google_storage_bucket.network-analyzer-attestations[0].name}",
        "--build-remote-identity=${google_service_account.network-analyzer-build[0].name}",
        "--logs-bucket=${google_storage_bucket.logs.name}",
        "--metadata-bucket=${google_storage_bucket.metadata.name}",
        "--attestation-bucket=${google_storage_bucket.attestations.name}",
        "--debug-storage=gs://${google_storage_bucket.debug.name}",
        "--signing-key-version=${data.google_kms_crypto_key_version.signing-key-version.name}",
        "--verifying-key-version=${data.google_kms_crypto_key_version.signing-key-version.name}",
        "--overwrite-attestations=false",
        ], var.enable_private_build_pool ? [
        "--gcb-private-pool-name=${google_cloudbuild_worker_pool.private-pool[0].id}",
        "--gcb-private-pool-region=us-central1",
      ] : [])
      resources {
        limits = {
          cpu    = "1000m"
          memory = "2G"
        }
      }
    }
    scaling { max_instance_count = 10 }
  }
  depends_on = [google_project_service.run]
}
resource "google_cloud_run_v2_service" "network-subscriber" {
  count    = var.enable_network_analyzer ? 1 : 0
  name     = "network-subscriber"
  location = "us-central1"
  template {
    service_account = google_service_account.network-analyzer[0].email
    timeout         = "${2 * 60}s" // 2 minutes
    containers {
      image = data.google_artifact_registry_docker_image.network-subscriber[0].self_link
      args = [
        "--analyzer-url=${google_cloud_run_v2_service.network-analyzer[0].uri}",
        "--task-queue=${google_cloud_tasks_queue.network-analyzer-queue[0].id}",
        "--task-queue-email=${google_service_account.network-analyzer[0].email}",
      ]
      resources {
        limits = {
          cpu    = "1000m"
          memory = "512Mi"
        }
      }
    }
    scaling { max_instance_count = 1 }
  }
  depends_on = [google_project_service.run]
}
resource "google_cloud_run_v2_service" "agent-api" {
  name     = "agent-api"
  location = "us-central1"
  template {
    service_account = google_service_account.orchestrator.email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.agent-api.self_link
      args = concat([
        "--project=${var.project}",
        "--build-remote-identity=${google_service_account.builder-agent.name}",
        "--logs-bucket=${google_storage_bucket.agent-logs.name}",
        "--metadata-bucket=${google_storage_bucket.agent-metadata.name}",
        "--prebuild-bucket=${google_storage_bucket.bootstrap-tools.name}",
        "--prebuild-version=${var.prebuild_version}",
        "--prebuild-auth=${var.public ? "false" : "true"}",
        ], var.enable_private_build_pool ? [
        "--gcb-private-pool-name=${google_cloudbuild_worker_pool.private-pool[0].id}",
        "--gcb-private-pool-region=us-central1",
      ] : [])
      resources {
        limits = {
          cpu    = "1000m"
          memory = "1G"
        }
      }
    }
    scaling { max_instance_count = 10 }
  }
  depends_on = [google_project_service.run]
}

resource "google_cloud_run_v2_job" "agent" {
  name     = "agent"
  location = "us-central1"
  deletion_protection = false
  template {
    template {
      service_account = google_service_account.agent-job.email
      timeout         = "3600s" // 1 hour default
      max_retries     = 0
      containers {
        image = data.google_artifact_registry_docker_image.agent.self_link
        resources {
          limits = {
            cpu    = "1000m"
            memory = "4G"
          }
        }
      }
    }
  }
  lifecycle {
    ignore_changes = [
      launch_stage,
    ]
  }
  depends_on = [google_project_service.run]
}

## IAM Bindings

resource "google_project_iam_custom_role" "bucket-viewer-role" {
  role_id     = "bucketViewer"
  title       = "Bucket Viewer"
  permissions = ["storage.buckets.get", "storage.buckets.list"]
}
resource "google_storage_bucket_iam_binding" "git-cache-manages-git-cache" {
  bucket  = google_storage_bucket.git-cache.name
  role    = "roles/storage.objectAdmin"
  members = ["serviceAccount:${google_service_account.git-cache.email}"]
}
resource "google_storage_bucket_iam_binding" "local-build-reads-git-cache" {
  bucket  = google_storage_bucket.git-cache.name
  role    = "roles/storage.objectViewer"
  members = ["serviceAccount:${google_service_account.builder-local.email}"]
}
resource "google_storage_bucket_iam_binding" "orchestrator-writes-attestations" {
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectCreator"
  members = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_storage_bucket_iam_binding" "attestors-manage-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectAdmin"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_storage_bucket_iam_binding" "attestors-and-local-build-write-debug" {
  bucket = google_storage_bucket.debug.name
  role   = "roles/storage.objectCreator"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    "serviceAccount:${google_service_account.builder-local.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_storage_bucket_iam_binding" "builders-write-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectCreator"
  members = concat([
    "serviceAccount:${google_service_account.builder-remote.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer-build[0].email}",
  ] : [])
}
resource "google_storage_bucket_iam_binding" "builders-use-logs" {
  bucket = google_storage_bucket.logs.name
  role   = "roles/storage.objectUser"
  members = concat([
    "serviceAccount:${google_service_account.builder-remote.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer-build[0].email}",
  ] : [])
}
resource "google_storage_bucket_iam_binding" "builders-view-logs" {
  bucket = google_storage_bucket.logs.name
  role   = google_project_iam_custom_role.bucket-viewer-role.name
  members = concat([
    "serviceAccount:${google_service_account.builder-remote.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer-build[0].email}",
  ] : [])
}
resource "google_storage_bucket_iam_binding" "orchestrator-manages-attestations" {
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectAdmin"
  members = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_project_iam_binding" "orchestrator-uses-datastore" {
  project = var.project
  role    = "roles/datastore.user"
  members = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_storage_bucket_iam_binding" "builders-view-bootstrap-bucket" {
  count  = var.public ? 0 : 1 // NOTE: Non-public objects must still be visible to the builder.
  bucket = google_storage_bucket.bootstrap-tools.name
  role   = "roles/storage.objectViewer"
  members = concat([
    "serviceAccount:${google_service_account.builder-remote.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer-build[0].email}",
  ] : [])
}
resource "google_cloud_run_v2_service_iam_binding" "orchestrator-calls-build-local" {
  location = google_cloud_run_v2_service.build-local.location
  project  = google_cloud_run_v2_service.build-local.project
  name     = google_cloud_run_v2_service.build-local.name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_project_iam_binding" "orchestrators-run-workloads-as-others" {
  project = var.project
  role    = "roles/iam.serviceAccountUser"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_project_iam_binding" "orchestrators-run-gcb-builds" {
  project = var.project
  role    = "roles/cloudbuild.builds.editor"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_cloud_run_v2_service_iam_binding" "orchestrator-calls-inference" {
  location = google_cloud_run_v2_service.inference.location
  project  = google_cloud_run_v2_service.inference.project
  name     = google_cloud_run_v2_service.inference.name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_kms_crypto_key_iam_binding" "attestors-read-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.viewer"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_kms_crypto_key_iam_binding" "attestors-uses-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.signerVerifier"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
resource "google_cloud_run_v2_service_iam_binding" "local-build-calls-git-cache" {
  location = google_cloud_run_v2_service.git-cache.location
  project  = google_cloud_run_v2_service.git-cache.project
  name     = google_cloud_run_v2_service.git-cache.name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.builder-local.email}"]
}
resource "google_cloud_run_v2_service_iam_binding" "api-and-local-build-and-inference-call-gateway" {
  location = google_cloud_run_v2_service.gateway.location
  project  = google_cloud_run_v2_service.gateway.project
  name     = google_cloud_run_v2_service.gateway.name
  role     = "roles/run.invoker"
  members = [
    "serviceAccount:${google_service_account.builder-local.email}",
    "serviceAccount:${google_service_account.inference.email}",
    "serviceAccount:${google_service_account.orchestrator.email}",
  ]
}
resource "google_pubsub_topic_iam_binding" "can-publish-to-attestation-topic" {
  topic   = google_pubsub_topic.attestation-topic.id
  role    = "roles/pubsub.publisher"
  members = ["serviceAccount:${data.google_storage_project_service_account.attestation-pubsub-publisher.email_address}"]
}
resource "google_pubsub_topic_iam_binding" "readers-can-read-from-attestation-topic" {
  count   = var.public ? 0 : 1
  topic   = google_pubsub_topic.attestation-topic.id
  role    = "roles/pubsub.subscriber"
  members = ["serviceAccount:${google_service_account.attestation-pubsub-reader[0].email}"]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-writes-analysis" {
  count   = var.enable_network_analyzer ? 1 : 0
  bucket  = google_storage_bucket.network-analyzer-attestations[0].name
  role    = "roles/storage.objectCreator"
  members = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-reads-attestations" {
  count   = !var.public && var.enable_network_analyzer ? 1 : 0
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectViewer"
  members = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-reads-network-analyzer-attestations" {
  count   = !var.public && var.enable_network_analyzer ? 1 : 0
  bucket  = google_storage_bucket.network-analyzer-attestations[0].name
  role    = "roles/storage.objectViewer"
  members = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_cloud_tasks_queue_iam_binding" "network-analyzer-enqueues-tasks" {
  count   = var.enable_network_analyzer ? 1 : 0
  name    = google_cloud_tasks_queue.network-analyzer-queue[0].name
  role    = "roles/cloudtasks.enqueuer"
  members = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_cloud_run_v2_service_iam_binding" "network-analyzer-calls-analyzer" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-analyzer[0].location
  project  = google_cloud_run_v2_service.network-analyzer[0].project
  name     = google_cloud_run_v2_service.network-analyzer[0].name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_cloud_run_v2_service_iam_binding" "network-analyzer-calls-subscriber" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-subscriber[0].location
  project  = google_cloud_run_v2_service.network-subscriber[0].project
  name     = google_cloud_run_v2_service.network-subscriber[0].name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_service_account_iam_binding" "network-analyzer-acts-as-itself" {
  count              = var.enable_network_analyzer ? 1 : 0
  service_account_id = google_service_account.network-analyzer[0].name
  role               = "roles/iam.serviceAccountUser"
  members            = ["serviceAccount:${google_service_account.network-analyzer[0].email}"]
}
resource "google_project_iam_binding" "orchestrators-use-worker-pool" {
  count   = var.enable_private_build_pool ? 1 : 0
  project = var.project
  role    = "roles/cloudbuild.workerPoolUser"
  members = concat([
    "serviceAccount:${google_service_account.orchestrator.email}",
    ], var.enable_network_analyzer ? [
    "serviceAccount:${google_service_account.network-analyzer[0].email}",
  ] : [])
}
# Agent-specific IAM bindings
resource "google_project_iam_binding" "agent-uses-aiplatform" {
  project = var.project
  role    = "roles/aiplatform.user"
  members = ["serviceAccount:${google_service_account.agent-job.email}"]
}
resource "google_storage_bucket_iam_binding" "agent-manages-sessions" {
  bucket  = google_storage_bucket.agent-sessions.name
  role    = "roles/storage.objectCreator"
  members = ["serviceAccount:${google_service_account.agent-job.email}"]
}
resource "google_storage_bucket_iam_binding" "agent-reads-metadata" {
  bucket  = google_storage_bucket.agent-metadata.name
  role    = "roles/storage.objectViewer"
  members = ["serviceAccount:${google_service_account.agent-job.email}"]
}
resource "google_project_iam_binding" "orchestrator-creates-run-jobs" {
  project = var.project
  role    = "roles/run.jobsExecutorWithOverrides"
  members = ["serviceAccount:${google_service_account.orchestrator.email}"]
}
resource "google_cloud_run_v2_service_iam_binding" "agent-calls-agent-api" {
  location = google_cloud_run_v2_service.agent-api.location
  project  = google_cloud_run_v2_service.agent-api.project
  name     = google_cloud_run_v2_service.agent-api.name
  role     = "roles/run.invoker"
  members  = ["serviceAccount:${google_service_account.agent-job.email}"]
}
resource "google_storage_bucket_iam_binding" "builder-agent-uses-logs" {
  bucket  = google_storage_bucket.agent-logs.name
  role   = "roles/storage.objectUser"
  members = ["serviceAccount:${google_service_account.builder-agent.email}"]
}
resource "google_storage_bucket_iam_binding" "builder-agent-writes-metadata" {
  bucket  = google_storage_bucket.agent-metadata.name
  role    = "roles/storage.objectCreator"
  members = ["serviceAccount:${google_service_account.builder-agent.email}"]
}

## Public resources

resource "google_kms_crypto_key_iam_binding" "signing-key-is-public" {
  count         = var.public ? 1 : 0
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.verifier"
  members       = ["allUsers"]
}
resource "google_storage_bucket_iam_binding" "attestation-bucket-is-public" {
  count   = var.public ? 1 : 0
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectViewer"
  members = ["allUsers"]
}
resource "google_storage_bucket_iam_binding" "bootstrap-bucket-is-public" {
  count   = var.public ? 1 : 0
  bucket  = google_storage_bucket.bootstrap-tools.name
  role    = "roles/storage.objectViewer"
  members = ["allUsers"]
}
resource "google_pubsub_topic_iam_binding" "attestation-bucket-topic-is-public" {
  count   = var.public ? 1 : 0
  topic   = google_pubsub_topic.attestation-topic.id
  role    = "roles/pubsub.subscriber"
  members = ["allUsers"]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-attestations-bucket-is-public" {
  count   = var.enable_network_analyzer && var.public ? 1 : 0
  bucket  = google_storage_bucket.network-analyzer-attestations[0].name
  role    = "roles/storage.objectViewer"
  members = ["allUsers"]
}
