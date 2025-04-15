# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

variable "project" {
  type        = string
  description = "Google Cloud project ID to be used to deploy resources"
}
variable "analyzer" {
  type        = string
  description = "Name of the analyzer"
}
variable "attestation_topic" {
  type        = string
  description = "Resource name of the source attestation topic"
  default     = "projects/oss-rebuild/topics/oss-rebuild-attestation-topic"
  validation {
    condition = can(regex((
      "^projects/(?:[a-zA-Z][a-zA-Z0-9+-]*)/topics/(?:[a-zA-Z][a-zA-Z0-9+-]*)$"
    ), var.attestation_topic))
    error_message = "Attestation topic must have a valid resource name."
  }
}
variable "repo" {
  type        = string
  description = "Repository URI to be resolved for building and deploying services"
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
variable "public" {
  type        = bool
  description = "Whether to enable public access to certain resources like attestations resources."
  default     = true
}
variable "debug" {
  type        = bool
  description = "Whether to build and deploy services from debug builds."
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

resource "google_service_account" "analyzer" {
  account_id  = "analyzer"
  description = "Primary API identity for the rebuilder. NOTE: This should NOT be used to run untrusted code."
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

resource "google_project_service" "storage" {
  service = "storage.googleapis.com"
}
resource "google_storage_bucket" "findings" {
  name                        = "${var.analyzer}-rebuild-analyzer-attestations"
  location                    = "us-central1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  depends_on                  = [google_project_service.storage]
}
data "google_storage_bucket" "rebuild-attestations" {
  name       = var.rebuild_attestation_bucket
  depends_on = [google_project_service.storage]
}

## PubSub

resource "google_project_service" "pubsub" {
  service = "pubsub.googleapis.com"
}
data "google_pubsub_topic" "attestation-topic" {
  name       = var.attestation_topic
  depends_on = [google_project_service.pubsub]
}

resource "google_cloud_tasks_queue" "analyzer-queue" {
  name     = "${var.analyzer}-analyzer-queue"
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
}

resource "google_pubsub_subscription" "rebuild-feed" {
  name  = "rebuild-feed"
  topic = data.google_pubsub_topic.attestation-topic.id
  push_config {
    push_endpoint = "${google_cloud_run_v2_service.analyzer.uri}/enqueue"
    no_wrapper    = true # NOTE: PubSub metadata will not be included
    oidc_token {
      service_account_email = google_service_account.analyzer.email
    }
  }
  message_retention_duration = "${7 * 24 * 60 * 60}s" # 7 days
  ack_deadline_seconds       = 600
}

## Container resources

resource "google_artifact_registry_repository" "registry" {
  location      = "us-central1"
  repository_id = "${var.analyzer}-analyzer-images"
  format        = "DOCKER"
  docker_config {
    immutable_tags = true
  }
}

resource "terraform_data" "service_version" {
  input = var.service_version
}

resource "terraform_data" "debug" {
  input = var.debug
}

resource "terraform_data" "git_dir" {
  input = (
    !startswith(var.repo, "file://") ? "!remote!" :
    fileexists(join("/", [substr(var.repo, 7, -1), ".git/config"])) ? join("/", [substr(var.repo, 7, -1), ".git"]) :
    fileexists(join("/", [substr(var.repo, 7, -1), ".jj/repo/store/git/config"])) ? join("/", [substr(var.repo, 7, -1), ".jj/repo/store/git"]) :
  "")
}

locals {
  registry_url = "${google_artifact_registry_repository.registry.location}-docker.pkg.dev/${var.project}/${google_artifact_registry_repository.registry.repository_id}"
  # Add .git suffix if it's a GitHub URL and doesn't already end with .git
  repo_with_git = (
    can(regex("^https://github\\.com/", var.repo)) && !endswith(var.repo, ".git") ? "${var.repo}.git" : var.repo
  )
  repo_docker_context = (
    startswith(var.repo, "file:")
    ? "- < <(GIT_DIR=${terraform_data.git_dir.output} git archive --format=tar ${var.service_commit})"
  : "${local.repo_with_git}#${var.service_commit}")
}

resource "terraform_data" "image" {
  for_each = {
    "analyzer" = {
      name      = "exampleanalyzer"
      image     = "${local.registry_url}/analyzer"
      version   = terraform_data.service_version.output
      buildargs = ["DEBUG=${terraform_data.debug.output}", "BUILD_REPO=${var.repo}", "BUILD_VERSION=${terraform_data.service_version.output}"]
    }
    "analyzer" = {
      name    = "examplesubscriber"
      image   = "${local.registry_url}/subscriber"
      version = terraform_data.service_version.output
    }
  }
  provisioner "local-exec" {
    command = <<-EOT
      path=${each.value.image}:${each.value.version}
      cmd="gcloud artifacts docker images describe $path"
      # Suppress stdout, show first line of stderr, return cmd's status.
      if ($cmd 2>&1 1>/dev/null | head -n1 >&2; exit $PIPESTATUS); then
        echo Found $path
      else
        echo Building $path
        docker build --quiet ${join(" ", [for arg in each.value.buildargs : "--build-arg ${arg}"])} -f build/package/Dockerfile.${each.value.name} -t $path ${local.repo_docker_context} && \
          docker push --quiet $path
      fi
    EOT
  }
  lifecycle {
    replace_triggered_by = [
      terraform_data.service_version.output,
      terraform_data.git_dir.output,
    ]
  }
}

data "google_artifact_registry_docker_image" "analyzer" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "analyzer:${terraform_data.service_version.output}"
  depends_on    = [terraform_data.image["analyzer"]]
}

data "google_artifact_registry_docker_image" "subscriber" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "subscriber:${terraform_data.service_version.output}"
  depends_on    = [terraform_data.image["subscriber"]]
}

## Compute resources

resource "google_project_service" "run" {
  service = "run.googleapis.com"
}
resource "google_cloud_run_v2_service" "analyzer" {
  name     = "${var.analyzer}-analyzer"
  location = "us-central1"
  template {
    service_account = google_service_account.analyzer.email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.analyzer.self_link
      args = [
        "--findings-bucket=${google_storage_bucket.findings.name}",
      ]
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
resource "google_cloud_run_v2_service" "subscriber" {
  name     = "${var.analyzer}-subscriber"
  location = "us-central1"
  template {
    service_account = google_service_account.analyzer.email
    timeout         = "${2 * 60}s" // 2 minutes
    containers {
      image = data.google_artifact_registry_docker_image.subscriber.self_link
      args = [
        "--analyzer-url=${google_cloud_run_v2_service.analyzer.uri}",
        "--task-queue=${google_cloud_tasks_queue.analyzer-queue.id}",
        "--task-queue-email=${google_service_account.analyzer.email}",
      ]
      resources {
        limits = {
          cpu    = "500m"
          memory = "500m"
        }
      }
    }
    scaling { max_instance_count = 10 }
  }
  depends_on = [google_project_service.run]
}

## IAM Bindings

resource "google_storage_bucket_iam_binding" "analyzer-writes-findings" {
  bucket  = google_storage_bucket.findings.name
  role    = "roles/storage.objectCreator"
  members = ["serviceAccount:${google_service_account.analyzer.email}"]
}
resource "google_kms_crypto_key_iam_binding" "analyzer-reads-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.viewer"
  members       = ["serviceAccount:${google_service_account.analyzer.email}"]
}
resource "google_kms_crypto_key_iam_binding" "analyzer-uses-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.signerVerifier"
  members       = ["serviceAccount:${google_service_account.analyzer.email}"]
}
resource "google_cloud_tasks_queue_iam_binding" "analyzer-enqueues-tasks" {
  name    = google_cloud_tasks_queue.analyzer-queue.name
  role    = "roles/cloudtasks.enqueuer"
  members = ["serviceAccount:${google_service_account.analyzer.email}"]
}
resource "google_cloud_run_v2_service_iam_binding" "analyzer-calls-itself" {
  name    = google_cloud_run_v2_service.analyzer.name
  role    = "roles/run.invoker"
  members = ["serviceAccount:${google_service_account.analyzer.email}"]
}
resource "google_service_account_iam_member" "analyzer-can-act-as-itself" {
  service_account_id = google_service_account.analyzer.name
  role               = "roles/iam.serviceAccountUser"
  members            = ["serviceAccount:${google_service_account.analyzer.email}"]
}

## Public resources

resource "google_kms_crypto_key_iam_binding" "signing-key-is-public" {
  count         = var.public ? 1 : 0
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.verifier"
  members       = ["allUsers"]
}
resource "google_storage_bucket_iam_binding" "findings-bucket-is-public" {
  count   = var.public ? 1 : 0
  bucket  = google_storage_bucket.findings.name
  role    = "roles/storage.objectViewer"
  members = ["allUsers"]
}
