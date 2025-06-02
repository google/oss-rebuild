# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

variable "source_image_url" {
  description = "Full Docker image URL with tag"
  type        = string
}

variable "binary_name" {
  description = "Name of binary to extract from container"
  type        = string
}

variable "gcs_destination" {
  description = "GCS URI where binary should be uploaded (e.g. gs://bucket/path)"
  type        = string
}
