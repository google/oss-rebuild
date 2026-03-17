# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

## Service accounts

resource "google_service_account" "orchestrator" {
  account_id  = "orchestrator"
  description = "Primary API identity for the rebuilder. NOTE: This should NOT be used to run rebuilds."
}
resource "google_service_account" "builder-remote" {
  account_id  = "builder-remote"
  description = "Rebuild identity used to run rebuilds executed remotely from the RPC service node."
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
resource "google_service_account" "crates-registry" {
  account_id  = "crates-registry"
  description = "Identity serving crates registry commit resolution endpoint."
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
resource "google_service_account" "system-analyzer" {
  count       = var.enable_system_analyzer ? 1 : 0
  account_id  = "system-analyzer"
  description = "Primary API identity for the system analyzer"
}
resource "google_service_account" "system-analyzer-build" {
  count       = var.enable_system_analyzer ? 1 : 0
  account_id  = "system-analyzer-build"
  description = "Build identity for the system analyzer"
}
data "google_storage_project_service_account" "attestation-pubsub-publisher" {
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
  members = [google_service_account.git-cache.member]
}
resource "google_storage_bucket_iam_binding" "git-cache-views-git-cache" {
  bucket  = google_storage_bucket.git-cache.name
  role    = google_project_iam_custom_role.bucket-viewer-role.name
  members = [google_service_account.git-cache.member]
}
resource "google_storage_bucket_iam_binding" "cachers-read-git-cache" {
  bucket = google_storage_bucket.git-cache.name
  role   = "roles/storage.objectViewer"
  members = [
    google_service_account.crates-registry.member,
  ]
}
resource "google_storage_bucket_iam_binding" "orchestrator-writes-attestations" {
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectCreator"
  members = [google_service_account.orchestrator.member]
}
resource "google_storage_bucket_iam_binding" "analyzers-read-attestations" {
  count  = !var.public && (var.enable_network_analyzer || var.enable_system_analyzer) ? 1 : 0
  bucket = google_storage_bucket.attestations.name
  role   = "roles/storage.objectViewer"
  members = concat(
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : [],
  )
}
resource "google_storage_bucket_iam_binding" "attestors-manage-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectAdmin"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_storage_bucket_iam_binding" "attestors-write-debug" {
  bucket = google_storage_bucket.debug.name
  role   = "roles/storage.objectCreator"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_storage_bucket_iam_binding" "builders-write-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectCreator"
  members = concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  )
}
resource "google_storage_bucket_iam_binding" "builders-use-logs" {
  bucket = google_storage_bucket.logs.name
  role   = "roles/storage.objectUser"
  members = concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  )
}
resource "google_storage_bucket_iam_binding" "builders-view-logs" {
  bucket = google_storage_bucket.logs.name
  role   = google_project_iam_custom_role.bucket-viewer-role.name
  members = concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  )
}
resource "google_storage_bucket_iam_binding" "orchestrator-manages-attestations" {
  bucket  = google_storage_bucket.attestations.name
  role    = "roles/storage.objectAdmin"
  members = [google_service_account.orchestrator.member]
}
resource "google_project_iam_binding" "orchestrator-uses-datastore" {
  project = var.project
  role    = "roles/datastore.user"
  members = [google_service_account.orchestrator.member]
}
resource "google_storage_bucket_iam_binding" "builders-view-bootstrap-bucket" {
  count  = var.public ? 0 : 1 // NOTE: Non-public objects must still be visible to the builder.
  bucket = google_storage_bucket.bootstrap-tools.name
  role   = "roles/storage.objectViewer"
  members = concat([
    google_service_account.builder-remote.member,
    google_service_account.builder-agent.member,
    ],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  )
}
resource "google_project_iam_binding" "orchestrators-run-workloads-as-others" {
  project = var.project
  role    = "roles/iam.serviceAccountUser"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_project_iam_binding" "orchestrators-run-gcb-builds" {
  project = var.project
  role    = "roles/cloudbuild.builds.editor"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_cloud_run_v2_service_iam_binding" "orchestrator-calls-inference" {
  location = google_cloud_run_v2_service.inference.location
  project  = google_cloud_run_v2_service.inference.project
  name     = google_cloud_run_v2_service.inference.name
  role     = "roles/run.invoker"
  members  = [google_service_account.orchestrator.member]
}
resource "google_cloud_run_v2_service_iam_binding" "inference-calls-crates-registry" {
  location = google_cloud_run_v2_service.crates-registry.location
  project  = google_cloud_run_v2_service.crates-registry.project
  name     = google_cloud_run_v2_service.crates-registry.name
  role     = "roles/run.invoker"
  members  = [google_service_account.inference.member]
}
resource "google_kms_crypto_key_iam_binding" "attestors-read-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.viewer"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_kms_crypto_key_iam_binding" "attestors-uses-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.signerVerifier"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
resource "google_cloud_run_v2_service_iam_binding" "cachers-call-git-cache" {
  location = google_cloud_run_v2_service.git-cache.location
  project  = google_cloud_run_v2_service.git-cache.project
  name     = google_cloud_run_v2_service.git-cache.name
  role     = "roles/run.invoker"
  members = [
    google_service_account.crates-registry.member,
  ]
}
resource "google_cloud_run_v2_service_iam_binding" "api-and-inference-call-gateway" {
  location = google_cloud_run_v2_service.gateway.location
  project  = google_cloud_run_v2_service.gateway.project
  name     = google_cloud_run_v2_service.gateway.name
  role     = "roles/run.invoker"
  members = [
    google_service_account.inference.member,
    google_service_account.orchestrator.member,
  ]
}
resource "google_pubsub_topic_iam_binding" "can-publish-to-attestation-topic" {
  topic   = google_pubsub_topic.attestation-topic.id
  role    = "roles/pubsub.publisher"
  members = [data.google_storage_project_service_account.attestation-pubsub-publisher.member]
}
resource "google_pubsub_topic_iam_binding" "readers-can-read-from-attestation-topic" {
  count   = var.public ? 0 : 1
  topic   = google_pubsub_topic.attestation-topic.id
  role    = "roles/pubsub.subscriber"
  members = [google_service_account.attestation-pubsub-reader[0].member]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-writes-analysis" {
  count   = var.enable_network_analyzer ? 1 : 0
  bucket  = google_storage_bucket.network-analyzer-attestations[0].name
  role    = "roles/storage.objectCreator"
  members = [google_service_account.network-analyzer[0].member]
}
resource "google_storage_bucket_iam_binding" "network-analyzer-reads-network-analyzer-attestations" {
  count   = !var.public && var.enable_network_analyzer ? 1 : 0
  bucket  = google_storage_bucket.network-analyzer-attestations[0].name
  role    = "roles/storage.objectViewer"
  members = [google_service_account.network-analyzer[0].member]
}
resource "google_cloud_tasks_queue_iam_binding" "network-analyzer-enqueues-tasks" {
  count   = var.enable_network_analyzer ? 1 : 0
  name    = google_cloud_tasks_queue.network-analyzer-queue[0].name
  role    = "roles/cloudtasks.enqueuer"
  members = [google_service_account.network-analyzer[0].member]
}
resource "google_cloud_run_v2_service_iam_binding" "network-analyzer-calls-analyzer" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-analyzer[0].location
  project  = google_cloud_run_v2_service.network-analyzer[0].project
  name     = google_cloud_run_v2_service.network-analyzer[0].name
  role     = "roles/run.invoker"
  members  = [google_service_account.network-analyzer[0].member]
}
resource "google_cloud_run_v2_service_iam_binding" "network-analyzer-calls-subscriber" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-subscriber[0].location
  project  = google_cloud_run_v2_service.network-subscriber[0].project
  name     = google_cloud_run_v2_service.network-subscriber[0].name
  role     = "roles/run.invoker"
  members  = [google_service_account.network-analyzer[0].member]
}
resource "google_service_account_iam_binding" "network-analyzer-acts-as-itself" {
  count              = var.enable_network_analyzer ? 1 : 0
  service_account_id = google_service_account.network-analyzer[0].name
  role               = "roles/iam.serviceAccountUser"
  members            = [google_service_account.network-analyzer[0].member]
}
# System analyzer-specific IAM bindings
resource "google_storage_bucket_iam_binding" "system-analyzer-writes-analysis" {
  count   = var.enable_system_analyzer ? 1 : 0
  bucket  = google_storage_bucket.system-analyzer-attestations[0].name
  role    = "roles/storage.objectCreator"
  members = [google_service_account.system-analyzer[0].member]
}
resource "google_storage_bucket_iam_binding" "system-analyzer-reads-system-analyzer-attestations" {
  count   = !var.public && var.enable_system_analyzer ? 1 : 0
  bucket  = google_storage_bucket.system-analyzer-attestations[0].name
  role    = "roles/storage.objectViewer"
  members = [google_service_account.system-analyzer[0].member]
}
resource "google_cloud_tasks_queue_iam_binding" "system-analyzer-enqueues-tasks" {
  count   = var.enable_system_analyzer ? 1 : 0
  name    = google_cloud_tasks_queue.system-analyzer-queue[0].name
  role    = "roles/cloudtasks.enqueuer"
  members = [google_service_account.system-analyzer[0].member]
}
resource "google_cloud_run_v2_service_iam_binding" "system-analyzer-calls-analyzer" {
  count    = var.enable_system_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.system-analyzer[0].location
  project  = google_cloud_run_v2_service.system-analyzer[0].project
  name     = google_cloud_run_v2_service.system-analyzer[0].name
  role     = "roles/run.invoker"
  members  = [google_service_account.system-analyzer[0].member]
}
resource "google_cloud_run_v2_service_iam_binding" "system-analyzer-calls-subscriber" {
  count    = var.enable_system_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.system-subscriber[0].location
  project  = google_cloud_run_v2_service.system-subscriber[0].project
  name     = google_cloud_run_v2_service.system-subscriber[0].name
  role     = "roles/run.invoker"
  members  = [google_service_account.system-analyzer[0].member]
}
resource "google_service_account_iam_binding" "system-analyzer-acts-as-itself" {
  count              = var.enable_system_analyzer ? 1 : 0
  service_account_id = google_service_account.system-analyzer[0].name
  role               = "roles/iam.serviceAccountUser"
  members            = [google_service_account.system-analyzer[0].member]
}
resource "google_project_iam_binding" "orchestrators-use-worker-pool" {
  count   = var.enable_private_build_pool ? 1 : 0
  project = var.project
  role    = "roles/cloudbuild.workerPoolUser"
  members = concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  )
}
# Agent-specific IAM bindings
resource "google_project_iam_binding" "agent-uses-aiplatform" {
  project = var.project
  role    = "roles/aiplatform.user"
  members = [google_service_account.agent-job.member]
}
resource "google_storage_bucket_iam_binding" "agent-manages-sessions" {
  bucket  = google_storage_bucket.agent-sessions.name
  role    = "roles/storage.objectCreator"
  members = [google_service_account.agent-job.member]
}
resource "google_storage_bucket_iam_binding" "agent-reads-metadata" {
  bucket = google_storage_bucket.agent-metadata.name
  role   = "roles/storage.objectViewer"
  members = [
    google_service_account.agent-job.member,
    google_service_account.orchestrator.member, # Orchestrator runs agent-api, needs to do comparison
  ]
}
resource "google_storage_bucket_iam_binding" "builder-agent-views-buckets" {
  bucket  = google_storage_bucket.agent-logs.name
  role    = google_project_iam_custom_role.bucket-viewer-role.name
  members = [google_service_account.builder-agent.member]
}
resource "google_storage_bucket_iam_binding" "builder-agent-uses-logs" {
  bucket  = google_storage_bucket.agent-logs.name
  role    = "roles/storage.objectUser"
  members = [google_service_account.builder-agent.member]
}
resource "google_storage_bucket_iam_binding" "agent-job-views-logs" {
  bucket  = google_storage_bucket.agent-logs.name
  role    = "roles/storage.objectViewer"
  members = [google_service_account.agent-job.member]
}
resource "google_project_iam_binding" "orchestrator-creates-run-jobs" {
  project = var.project
  role    = "roles/run.jobsExecutorWithOverrides"
  members = [google_service_account.orchestrator.member]
}
resource "google_cloud_run_v2_service_iam_binding" "agent-calls-agent-api" {
  location = google_cloud_run_v2_service.agent-api.location
  project  = google_cloud_run_v2_service.agent-api.project
  name     = google_cloud_run_v2_service.agent-api.name
  role     = "roles/run.invoker"
  members  = [google_service_account.agent-job.member]
}
resource "google_storage_bucket_iam_binding" "builder-agent-writes-metadata" {
  bucket  = google_storage_bucket.agent-metadata.name
  role    = "roles/storage.objectCreator"
  members = [google_service_account.builder-agent.member]
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
resource "google_storage_bucket_iam_binding" "system-analyzer-attestations-bucket-is-public" {
  count   = var.enable_system_analyzer && var.public ? 1 : 0
  bucket  = google_storage_bucket.system-analyzer-attestations[0].name
  role    = "roles/storage.objectViewer"
  members = ["allUsers"]
}
