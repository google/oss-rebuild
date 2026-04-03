# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

data "google_project" "project" {
  project_id = var.project
}

locals {
  project_num = data.google_project.project.number
}

terraform {
  required_providers {
    google = {
      source  = "google"
      version = "~> 7.0"
    }
  }
}
provider "google" {
  project = var.project
  region  = "us-central1"
  zone    = "us-central1-c"
}
