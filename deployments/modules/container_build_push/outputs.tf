# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

output "full_image_url" {
  description = "Complete image URL with tag"
  value       = "${var.image_url}:${var.image_version}"
}

output "image_url" {
  description = "Image URL without tag"
  value       = var.image_url
}

output "image_version" {
  description = "Image version/tag"
  value       = var.image_version
}

output "name" {
  description = "Service name"
  value       = var.name
}
