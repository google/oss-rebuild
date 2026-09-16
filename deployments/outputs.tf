# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

output "dashboard_address" {
  description = "External address of the dashboard load balancer. Point the dashboard_domain A record here."
  value       = one(google_compute_global_address.dashboard[*].address)
}
output "dashboard_url" {
  description = "URL the dashboard is served on once its certificate is issued."
  value       = var.enable_dashboard ? "https://${local.dashboard_host}/" : null
}
