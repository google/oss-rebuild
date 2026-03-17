# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

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
    crates-registry = {
      dockerfile = "build/package/Dockerfile.crates-registry"
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
    } : {}, var.enable_system_analyzer ? {
    system-analyzer = {
      dockerfile = "build/package/Dockerfile.systemanalyzer"
      build_args = [
        "DEBUG=${terraform_data.debug.output}",
        "BUILD_REPO=${var.repo}",
        "BUILD_VERSION=${terraform_data.service_version.output}"
      ]
    }
    system-subscriber = {
      dockerfile = "build/package/Dockerfile.systemsubscriber"
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
    tetragon_sysgraph = {
      dockerfile = "build/package/Dockerfile.tetragon_sysgraph"
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

data "google_artifact_registry_docker_image" "crates-registry" {
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "crates-registry:${module.service_images["crates-registry"].image_version}"
  depends_on    = [module.service_images["crates-registry"]]
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

data "google_artifact_registry_docker_image" "system-analyzer" {
  count         = var.enable_system_analyzer ? 1 : 0
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "system-analyzer:${module.service_images["system-analyzer"].image_version}"
  depends_on    = [module.service_images["system-analyzer"]]
}

data "google_artifact_registry_docker_image" "system-subscriber" {
  count         = var.enable_system_analyzer ? 1 : 0
  location      = google_artifact_registry_repository.registry.location
  repository_id = google_artifact_registry_repository.registry.repository_id
  image_name    = "system-subscriber:${module.service_images["system-subscriber"].image_version}"
  depends_on    = [module.service_images["system-subscriber"]]
}
