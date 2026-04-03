# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

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
  versioning {
    enabled = true
  }
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
resource "google_storage_bucket" "system-analyzer-attestations" {
  count                       = var.enable_system_analyzer ? 1 : 0
  name                        = "${var.host}-system-analyzer-attestations"
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

resource "google_cloud_tasks_queue" "system-analyzer-queue" {
  count    = var.enable_system_analyzer ? 1 : 0
  name     = "system-analyzer-queue"
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

resource "google_pubsub_subscription" "system-analyzer-feed" {
  count = var.enable_system_analyzer ? 1 : 0
  name  = "system-analyzer-feed"
  topic = google_pubsub_topic.attestation-topic.id
  push_config {
    push_endpoint = "${google_cloud_run_v2_service.system-subscriber[0].uri}/enqueue"
    no_wrapper {
      write_metadata = false
    }
    oidc_token {
      service_account_email = google_service_account.system-analyzer[0].email
    }
  }
  message_retention_duration = "${7 * 24 * 60 * 60}s" # 7 days
  ack_deadline_seconds       = 600
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
