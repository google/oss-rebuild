# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

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
resource "google_cloudbuild_worker_pool" "jumbo-pool" {
  count    = var.enable_private_build_pool ? 1 : 0
  name     = "${var.host}-rebuild-pool-jumbo"
  location = "us-central1"
  worker_config {
    machine_type = "e2-standard-32"
    disk_size_gb = 500
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

# Enumerates zones available in the scratch region so agent-api can fall
# through across all zones on stockout instead of being pinned to one.
data "google_compute_zones" "scratch" {
  count   = var.enable_scratch ? 1 : 0
  region  = "us-central1"
  status  = "UP"
  project = var.project
}

# Instance template for scratch VMs. The startup script fetches and runs the
# scratch-worker binary; agent-api drives the worker over private-IP HTTP with
# an ID token. Conditionally attach a service account for private instances to
# access (the bootstrap-tools bucket is public-readable).
resource "google_compute_instance_template" "scratch-standard" {
  count        = var.enable_scratch ? 1 : 0
  name_prefix  = "${var.host}-scratch-standard-"
  machine_type = "n2-standard-8"
  region       = "us-central1"

  service_account {
    email  = var.public ? "" : google_service_account.scratch-worker[0].email
    scopes = var.public ? [] : ["https://www.googleapis.com/auth/devstorage.read_only"]
  }

  disk {
    source_image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
    auto_delete  = true
    boot         = true
    disk_size_gb = 50
  }
  disk {
    type         = "SCRATCH"
    disk_type    = "local-ssd"
    auto_delete  = true
    interface    = "NVME"
    disk_size_gb = 375 # local SSD partitions are fixed at 375 GB on n2; add more disk blocks for more capacity.
  }

  network_interface {
    network    = google_compute_network.vpc[0].name
    subnetwork = google_compute_subnetwork.subnet[0].name
  }

  tags   = ["scratch"]
  labels = { purpose = "scratch" }

  metadata = {
    startup-script = templatefile("${path.module}/scratch_startup.sh", {
      worker_binary_uri = "gs://${google_storage_bucket.bootstrap-tools.name}/${module.prebuild_images["scratch-worker"].image_version}/scratch-worker"
      caller_sa         = google_service_account.orchestrator.email
      audience          = "https://builder/$${HOSTNAME}"
    })
  }

  lifecycle {
    create_before_destroy = true
  }
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
    timeout         = "${60 * 60}s" // 60 minutes
    containers {
      image = data.google_artifact_registry_docker_image.git-cache.self_link
      args = [
        "--cache=gs://${google_storage_bucket.git-cache.name}"
      ]
      resources {
        limits = {
          cpu    = "4000m"
          memory = "8G"
        }
      }
    }
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
        "--crates-registry-service-url=${google_cloud_run_v2_service.crates-registry.uri}",
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
resource "google_cloud_run_v2_service" "crates-registry" {
  name     = "crates-registry"
  location = "us-central1"
  template {
    service_account = google_service_account.crates-registry.email
    timeout         = "${45 * 60}s" // 45 minutes
    containers {
      image = data.google_artifact_registry_docker_image.crates-registry.self_link
      args = [
        "--cache-dir=/cache",
        "--max-snapshots=16",
        "--current-update-interval-mins=30",
        "--git-cache-url=${google_cloud_run_v2_service.git-cache.uri}",
      ]
      resources {
        limits = {
          cpu    = "4000m"
          memory = "16G"
        }
      }
      volume_mounts {
        name       = "cache"
        mount_path = "/cache"
      }
    }
    // At their current size, this should be enough to host the current index (1.5G) as well as ~16 snapshots (~.5G)
    volumes {
      name = "cache"
      empty_dir {
        medium     = "MEMORY"
        size_limit = "12Gi"
      }
    }
    max_instance_request_concurrency = 10
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
        "--location=us-central1",
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
        "--build-def-repo=${var.build_def_repo}",
        "--build-def-repo-dir=${var.build_def_repo_dir}",
        "--block-local-repo-publish=${var.public}",
        "--agent-job-name=${google_cloud_run_v2_job.agent.id}",
        "--agent-api-url=${google_cloud_run_v2_service.agent-api.uri}",
        "--agent-timeout-seconds=3600", // 1 hour
        "--agent-sessions-bucket=${google_storage_bucket.agent-sessions.name}",
        "--agent-metadata-bucket=${google_storage_bucket.agent-metadata.name}",
        "--agent-logs-bucket=${google_storage_bucket.agent-logs.name}",
        "--rebuild-job-name=${google_cloud_run_v2_job.rebuild-job.id}",
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
resource "google_cloud_run_v2_service" "system-analyzer" {
  count    = var.enable_system_analyzer ? 1 : 0
  name     = "system-analyzer"
  location = "us-central1"
  template {
    service_account = google_service_account.system-analyzer[0].email
    timeout         = "${59 * 60}s" // 59 minutes
    containers {
      image = data.google_artifact_registry_docker_image.system-analyzer[0].self_link
      args = concat([
        "--project=${var.project}",
        "--analysis-bucket=${google_storage_bucket.system-analyzer-attestations[0].name}",
        "--build-remote-identity=${google_service_account.system-analyzer-build[0].name}",
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
resource "google_cloud_run_v2_service" "system-subscriber" {
  count    = var.enable_system_analyzer ? 1 : 0
  name     = "system-subscriber"
  location = "us-central1"
  template {
    service_account = google_service_account.system-analyzer[0].email
    timeout         = "${2 * 60}s" // 2 minutes
    containers {
      image = data.google_artifact_registry_docker_image.system-subscriber[0].self_link
      args = [
        "--analyzer-url=${google_cloud_run_v2_service.system-analyzer[0].uri}",
        "--task-queue=${google_cloud_tasks_queue.system-analyzer-queue[0].id}",
        "--task-queue-email=${google_service_account.system-analyzer[0].email}",
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
        ] : [], var.enable_scratch ? [
        "--scratch-enabled=true",
        "--scratch-zones=${join(",", data.google_compute_zones.scratch[0].names)}",
        "--scratch-worker-port=8080",
        "--scratch-instance-standard-template=${google_compute_instance_template.scratch-standard[0].self_link}",
        "--scratch-output-bucket=${google_storage_bucket.scratch-output[0].name}",
      ] : [])
      resources {
        limits = {
          cpu    = "1000m"
          memory = "1G"
        }
      }
    }
    # When scratch is enabled, agent-api reaches worker VMs on private
    # IPs via Direct VPC egress.
    dynamic "vpc_access" {
      for_each = var.enable_scratch ? [1] : []
      content {
        network_interfaces {
          network    = google_compute_network.vpc[0].name
          subnetwork = google_compute_subnetwork.subnet[0].name
        }
        egress = "ALL_TRAFFIC"
      }
    }
    scaling { max_instance_count = 10 }
  }
  depends_on = [google_project_service.run]
}

resource "google_cloud_run_v2_job" "agent" {
  name                = "agent"
  location            = "us-central1"
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
            cpu    = "2000m"
            memory = "8G"
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

resource "google_cloud_run_v2_job" "rebuild-job" {
  name                = "rebuild-job"
  location            = "us-central1"
  deletion_protection = false
  template {
    template {
      service_account = google_service_account.orchestrator.email
      timeout         = "84600s" // 24 hour default
      max_retries     = 0
      containers {
        image = data.google_artifact_registry_docker_image.rebuild-job.self_link
        resources {
          limits = {
            cpu    = "1000m"
            memory = "512Mi"
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
