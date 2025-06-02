# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

variable "name" {
  description = "Service name"
  type        = string
}

variable "image_url" {
  description = "Full image URL without tag"
  type        = string
}

variable "image_version" {
  description = "Image version/tag"
  type        = string
}

variable "repo_url" {
  description = "Repository URL"
  type        = string
}

variable "commit" {
  description = "Git commit hash"
  type        = string
}

variable "dockerfile_path" {
  description = "Path to Dockerfile"
  type        = string
}

variable "build_args" {
  description = "Docker build arguments"
  type        = list(string)
  default     = []
}
