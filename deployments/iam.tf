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
resource "google_service_account" "scratch-worker" {
  count       = !var.public && var.enable_scratch ? 1 : 0
  account_id  = "scratch-worker"
  description = "Bootstrap identity for scratch worker VMs, used by the startup script to fetch the worker binary from the bootstrap-tools bucket on private deployments."
}
data "google_storage_project_service_account" "attestation-pubsub-publisher" {
}

## IAM Bindings

resource "google_project_iam_custom_role" "bucket-viewer-role" {
  role_id     = "bucketViewer"
  title       = "Bucket Viewer"
  permissions = ["storage.buckets.get", "storage.buckets.list"]
}
resource "google_storage_bucket_iam_member" "git-cache-manages-git-cache" {
  bucket = google_storage_bucket.git-cache.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.git-cache.member
}
resource "google_storage_bucket_iam_member" "git-cache-views-git-cache" {
  bucket = google_storage_bucket.git-cache.name
  role   = google_project_iam_custom_role.bucket-viewer-role.name
  member = google_service_account.git-cache.member
}
resource "google_storage_bucket_iam_member" "cachers-read-git-cache" {
  bucket = google_storage_bucket.git-cache.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.crates-registry.member
}
resource "google_storage_bucket_iam_member" "orchestrator-writes-attestations" {
  bucket = google_storage_bucket.attestations.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.orchestrator.member
}
resource "google_storage_bucket_iam_member" "analyzers-read-attestations" {
  bucket = google_storage_bucket.attestations.name
  role   = "roles/storage.objectViewer"
  for_each = toset(!var.public ? concat(
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : [],
  ) : [])
  member = each.key
}
resource "google_storage_bucket_iam_member" "attestors-manage-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectAdmin"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_storage_bucket_iam_member" "attestors-write-debug" {
  bucket = google_storage_bucket.debug.name
  role   = "roles/storage.objectCreator"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_storage_bucket_iam_member" "builders-write-metadata" {
  bucket = google_storage_bucket.metadata.name
  role   = "roles/storage.objectCreator"
  for_each = toset(concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  ))
  member = each.key
}
resource "google_storage_bucket_iam_member" "builders-use-logs" {
  bucket = google_storage_bucket.logs.name
  role   = "roles/storage.objectUser"
  for_each = toset(concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  ))
  member = each.key
}
resource "google_storage_bucket_iam_member" "builders-view-logs" {
  bucket = google_storage_bucket.logs.name
  role   = google_project_iam_custom_role.bucket-viewer-role.name
  for_each = toset(concat(
    [google_service_account.builder-remote.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : []
  ))
  member = each.key
}
resource "google_storage_bucket_iam_member" "orchestrator-manages-attestations" {
  bucket = google_storage_bucket.attestations.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.orchestrator.member
}
resource "google_project_iam_member" "orchestrator-uses-datastore" {
  project = var.project
  role    = "roles/datastore.user"
  member  = google_service_account.orchestrator.member
}
resource "google_storage_bucket_iam_member" "builders-view-bootstrap-bucket" {
  bucket = google_storage_bucket.bootstrap-tools.name
  role   = "roles/storage.objectViewer"
  for_each = toset(!var.public ? concat([
    google_service_account.builder-remote.member,
    google_service_account.builder-agent.member,
    ],
    var.enable_network_analyzer ? [google_service_account.network-analyzer-build[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer-build[0].member] : [],
    var.enable_scratch ? [google_service_account.scratch-worker[0].member] : []
  ) : [])
  member = each.key
}
resource "google_project_iam_member" "orchestrators-run-workloads-as-others" {
  project = var.project
  role    = "roles/iam.serviceAccountUser"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_project_iam_member" "orchestrators-run-gcb-builds" {
  project = var.project
  role    = "roles/cloudbuild.builds.editor"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_cloud_run_v2_service_iam_member" "orchestrator-calls-inference" {
  location = google_cloud_run_v2_service.inference.location
  project  = google_cloud_run_v2_service.inference.project
  name     = google_cloud_run_v2_service.inference.name
  role     = "roles/run.invoker"
  member   = google_service_account.orchestrator.member
}
resource "google_cloud_run_v2_service_iam_member" "inference-calls-crates-registry" {
  location = google_cloud_run_v2_service.crates-registry.location
  project  = google_cloud_run_v2_service.crates-registry.project
  name     = google_cloud_run_v2_service.crates-registry.name
  role     = "roles/run.invoker"
  member   = google_service_account.inference.member
}
resource "google_kms_crypto_key_iam_member" "attestors-read-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.viewer"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_kms_crypto_key_iam_member" "attestors-uses-signing-key" {
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.signerVerifier"
  for_each = toset(concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ))
  member = each.key
}
resource "google_cloud_run_v2_service_iam_member" "cachers-call-git-cache" {
  location = google_cloud_run_v2_service.git-cache.location
  project  = google_cloud_run_v2_service.git-cache.project
  name     = google_cloud_run_v2_service.git-cache.name
  role     = "roles/run.invoker"
  member   = google_service_account.crates-registry.member
}
resource "google_cloud_run_v2_service_iam_member" "api-and-inference-call-gateway" {
  location = google_cloud_run_v2_service.gateway.location
  project  = google_cloud_run_v2_service.gateway.project
  name     = google_cloud_run_v2_service.gateway.name
  role     = "roles/run.invoker"
  for_each = toset([
    google_service_account.inference.member,
    google_service_account.orchestrator.member,
  ])
  member = each.key
}
resource "google_pubsub_topic_iam_member" "can-publish-to-attestation-topic" {
  topic  = google_pubsub_topic.attestation-topic.id
  role   = "roles/pubsub.publisher"
  member = data.google_storage_project_service_account.attestation-pubsub-publisher.member
}
resource "google_pubsub_topic_iam_member" "readers-can-read-from-attestation-topic" {
  count  = var.public ? 0 : 1
  topic  = google_pubsub_topic.attestation-topic.id
  role   = "roles/pubsub.subscriber"
  member = google_service_account.attestation-pubsub-reader[0].member
}
resource "google_storage_bucket_iam_member" "network-analyzer-writes-analysis" {
  count  = var.enable_network_analyzer ? 1 : 0
  bucket = google_storage_bucket.network-analyzer-attestations[0].name
  role   = "roles/storage.objectCreator"
  member = google_service_account.network-analyzer[0].member
}
resource "google_storage_bucket_iam_member" "network-analyzer-reads-network-analyzer-attestations" {
  count  = !var.public && var.enable_network_analyzer ? 1 : 0
  bucket = google_storage_bucket.network-analyzer-attestations[0].name
  role   = "roles/storage.objectViewer"
  member = google_service_account.network-analyzer[0].member
}
resource "google_cloud_tasks_queue_iam_member" "network-analyzer-enqueues-tasks" {
  count  = var.enable_network_analyzer ? 1 : 0
  name   = google_cloud_tasks_queue.network-analyzer-queue[0].name
  role   = "roles/cloudtasks.enqueuer"
  member = google_service_account.network-analyzer[0].member
}
resource "google_cloud_run_v2_service_iam_member" "network-analyzer-calls-analyzer" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-analyzer[0].location
  project  = google_cloud_run_v2_service.network-analyzer[0].project
  name     = google_cloud_run_v2_service.network-analyzer[0].name
  role     = "roles/run.invoker"
  member   = google_service_account.network-analyzer[0].member
}
resource "google_cloud_run_v2_service_iam_member" "network-analyzer-calls-subscriber" {
  count    = var.enable_network_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.network-subscriber[0].location
  project  = google_cloud_run_v2_service.network-subscriber[0].project
  name     = google_cloud_run_v2_service.network-subscriber[0].name
  role     = "roles/run.invoker"
  member   = google_service_account.network-analyzer[0].member
}
resource "google_service_account_iam_member" "network-analyzer-acts-as-itself" {
  count              = var.enable_network_analyzer ? 1 : 0
  service_account_id = google_service_account.network-analyzer[0].name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.network-analyzer[0].member
}
# System analyzer-specific IAM bindings
resource "google_storage_bucket_iam_member" "system-analyzer-writes-analysis" {
  count  = var.enable_system_analyzer ? 1 : 0
  bucket = google_storage_bucket.system-analyzer-attestations[0].name
  role   = "roles/storage.objectCreator"
  member = google_service_account.system-analyzer[0].member
}
resource "google_storage_bucket_iam_member" "system-analyzer-reads-system-analyzer-attestations" {
  count  = !var.public && var.enable_system_analyzer ? 1 : 0
  bucket = google_storage_bucket.system-analyzer-attestations[0].name
  role   = "roles/storage.objectViewer"
  member = google_service_account.system-analyzer[0].member
}
resource "google_cloud_tasks_queue_iam_member" "system-analyzer-enqueues-tasks" {
  count  = var.enable_system_analyzer ? 1 : 0
  name   = google_cloud_tasks_queue.system-analyzer-queue[0].name
  role   = "roles/cloudtasks.enqueuer"
  member = google_service_account.system-analyzer[0].member
}
resource "google_cloud_run_v2_service_iam_member" "system-analyzer-calls-analyzer" {
  count    = var.enable_system_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.system-analyzer[0].location
  project  = google_cloud_run_v2_service.system-analyzer[0].project
  name     = google_cloud_run_v2_service.system-analyzer[0].name
  role     = "roles/run.invoker"
  member   = google_service_account.system-analyzer[0].member
}
resource "google_cloud_run_v2_service_iam_member" "system-analyzer-calls-subscriber" {
  count    = var.enable_system_analyzer ? 1 : 0
  location = google_cloud_run_v2_service.system-subscriber[0].location
  project  = google_cloud_run_v2_service.system-subscriber[0].project
  name     = google_cloud_run_v2_service.system-subscriber[0].name
  role     = "roles/run.invoker"
  member   = google_service_account.system-analyzer[0].member
}
resource "google_service_account_iam_member" "system-analyzer-acts-as-itself" {
  count              = var.enable_system_analyzer ? 1 : 0
  service_account_id = google_service_account.system-analyzer[0].name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.system-analyzer[0].member
}
resource "google_project_iam_member" "orchestrators-use-worker-pool" {
  project = var.project
  role    = "roles/cloudbuild.workerPoolUser"
  for_each = toset(var.enable_private_build_pool ? concat(
    [google_service_account.orchestrator.member],
    var.enable_network_analyzer ? [google_service_account.network-analyzer[0].member] : [],
    var.enable_system_analyzer ? [google_service_account.system-analyzer[0].member] : []
  ) : [])
  member = each.key
}
# Agent-specific IAM bindings
resource "google_project_iam_member" "agent-uses-aiplatform" {
  project = var.project
  role    = "roles/aiplatform.user"
  member  = google_service_account.agent-job.member
}
resource "google_storage_bucket_iam_member" "agent-manages-sessions" {
  bucket = google_storage_bucket.agent-sessions.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.agent-job.member
}
resource "google_storage_bucket_iam_member" "agent-reads-metadata" {
  bucket = google_storage_bucket.agent-metadata.name
  role   = "roles/storage.objectViewer"
  for_each = toset([
    google_service_account.agent-job.member,
    google_service_account.orchestrator.member, # Orchestrator runs agent-api, needs to do comparison
  ])
  member = each.key
}
resource "google_storage_bucket_iam_member" "builder-agent-views-buckets" {
  bucket = google_storage_bucket.agent-logs.name
  role   = google_project_iam_custom_role.bucket-viewer-role.name
  member = google_service_account.builder-agent.member
}
resource "google_storage_bucket_iam_member" "builder-agent-uses-logs" {
  bucket = google_storage_bucket.agent-logs.name
  role   = "roles/storage.objectUser"
  member = google_service_account.builder-agent.member
}
resource "google_storage_bucket_iam_member" "agent-job-views-logs" {
  bucket = google_storage_bucket.agent-logs.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.agent-job.member
}
resource "google_project_iam_member" "orchestrator-creates-run-jobs" {
  project = var.project
  role    = "roles/run.jobsExecutorWithOverrides"
  member  = google_service_account.orchestrator.member
}
resource "google_project_iam_member" "orchestrator-compute-admin" {
  count   = var.enable_scratch ? 1 : 0
  project = var.project
  role    = "roles/compute.instanceAdmin.v1"
  member  = google_service_account.orchestrator.member
}
resource "google_cloud_run_v2_service_iam_member" "agent-calls-agent-api" {
  location = google_cloud_run_v2_service.agent-api.location
  project  = google_cloud_run_v2_service.agent-api.project
  name     = google_cloud_run_v2_service.agent-api.name
  role     = "roles/run.invoker"
  member   = google_service_account.agent-job.member
}
resource "google_storage_bucket_iam_member" "builder-agent-writes-metadata" {
  bucket = google_storage_bucket.agent-metadata.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.builder-agent.member
}

## Public resources

resource "google_kms_crypto_key_iam_member" "signing-key-is-public" {
  count         = var.public ? 1 : 0
  crypto_key_id = google_kms_crypto_key.signing-key.id
  role          = "roles/cloudkms.verifier"
  member        = "allUsers"
}
resource "google_storage_bucket_iam_member" "attestation-bucket-is-public" {
  count  = var.public ? 1 : 0
  bucket = google_storage_bucket.attestations.name
  role   = "roles/storage.objectViewer"
  member = "allUsers"
}
resource "google_storage_bucket_iam_member" "bootstrap-bucket-is-public" {
  count  = var.public ? 1 : 0
  bucket = google_storage_bucket.bootstrap-tools.name
  role   = "roles/storage.objectViewer"
  member = "allUsers"
}
resource "google_pubsub_topic_iam_member" "attestation-bucket-topic-is-public" {
  count  = var.public ? 1 : 0
  topic  = google_pubsub_topic.attestation-topic.id
  role   = "roles/pubsub.subscriber"
  member = "allUsers"
}
resource "google_storage_bucket_iam_member" "network-analyzer-attestations-bucket-is-public" {
  count  = var.enable_network_analyzer && var.public ? 1 : 0
  bucket = google_storage_bucket.network-analyzer-attestations[0].name
  role   = "roles/storage.objectViewer"
  member = "allUsers"
}
resource "google_storage_bucket_iam_member" "system-analyzer-attestations-bucket-is-public" {
  count  = var.enable_system_analyzer && var.public ? 1 : 0
  bucket = google_storage_bucket.system-analyzer-attestations[0].name
  role   = "roles/storage.objectViewer"
  member = "allUsers"
}
