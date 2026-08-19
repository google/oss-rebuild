# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

resource "terraform_data" "extract_deps" {
  input = {
    source_image = var.source_image_url
    binary_name  = var.binary_name
    gcs_path     = var.gcs_destination
  }
}

resource "terraform_data" "extract" {
  input = terraform_data.extract_deps.output

  lifecycle {
    replace_triggered_by = [terraform_data.extract_deps]
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"] # needed for PIPESTATUS and pipefail
    command     = <<-EOT
      path=${var.gcs_destination}
      cmd="gcloud storage objects describe $path"
      # Suppress stdout, show first line of stderr, return cmd's status.
      if ($cmd 2>&1 1>/dev/null | head -n1 >&2; exit $PIPESTATUS); then
        echo "Binary already exists in GCS"
      else
        echo "Extracting and uploading binary"
        set -o pipefail
        cid=$(docker create ${var.source_image_url}) || exit 1
        docker cp "$cid:/${var.binary_name}" - | tar -xO | \
          gcloud storage cp - $path && \
          gcloud storage objects update $path --custom-metadata=goog-reserved-posix-mode=750
        status=$?
        docker rm "$cid" >/dev/null
        exit $status
      fi
    EOT
  }
}
