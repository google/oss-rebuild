# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

output "full_image_url" {
  description = "Complete image URL with tag"
  value       = "${terraform_data.image.output.image_url}:${terraform_data.image.output.image_version}"
}

output "image_url" {
  description = "Image URL without tag"
  value       = terraform_data.image.output.image_url
}

output "image_version" {
  description = "Image version/tag"
  value       = terraform_data.image.output.image_version
}

output "name" {
  description = "Service name"
  value       = terraform_data.image.output.name
}
