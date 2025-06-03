# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

resource "terraform_data" "extract" {
  input = {
    source_image = var.source_image_url
    binary_name = var.binary_name
    gcs_path = var.gcs_destination
  }

  provisioner "local-exec" {
    command = <<-EOT
      path=${var.gcs_destination}
      cmd="gcloud storage objects describe $path"
      # Suppress stdout, show first line of stderr, return cmd's status.
      if ($cmd 2>&1 1>/dev/null | head -n1 >&2; exit $PIPESTATUS); then
        echo "Binary already exists in GCS"
      else
        echo "Extracting and uploading binary"
        set -o pipefail
        docker save ${var.source_image_url} | \
          tar -xO --wildcards "*/layer.tar" | \
          tar -xO ${var.binary_name} | \
          gcloud storage cp - $path && \
          gcloud storage objects update $path --custom-metadata=goog-reserved-posix-mode=750
      fi
    EOT
  }
}
