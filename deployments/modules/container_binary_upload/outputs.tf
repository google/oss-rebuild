# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

output "gcs_path" {
  description = "GCS URI where binary was uploaded (e.g. gs://bucket/path)"
  value       = var.gcs_destination
}

output "binary_name" {
  description = "Name of the extracted binary"
  value       = var.binary_name
}
