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
    command = <<-EOT
      path=${var.gcs_destination}
      cmd="gcloud storage objects describe $path"
      # Suppress stdout, show first line of stderr, return cmd's status.
      if ($cmd 2>&1 1>/dev/null | head -n1 >&2; exit $PIPESTATUS); then
        echo "Binary already exists in GCS"
      else
        echo "Extracting and uploading binary"
        set -o pipefail
        image_archive_files=$(docker save ${var.source_image_url} | tar -t)
        if echo "$image_archive_files" | grep -q "index.json"; then  # OCI archive format
          manifest_path=$(docker save ${var.source_image_url} | tar -xO index.json | jq -r '.manifests[0].digest' | sed -E 's#(.+):(.+)#blobs/\1/\2#g')
          layer_paths=$(docker save ${var.source_image_url} | tar -xO "$manifest_path" | jq -r '.layers[].digest' | sed -E 's#(.+):(.+)#blobs/\1/\2#g')
        elif echo "$image_archive_files" | grep -q "manifest.json"; then  # Docker archive format
          layer_paths=$(docker save ${var.source_image_url} | tar -xO manifest.json | jq -r '.[0].Layers[]')
        else
          echo "Unknown archive format"
          exit 1
        fi
        for layer in $layer_paths; do
          if docker save ${var.source_image_url} | tar -xO "$layer" | tar -t | grep -q "${var.binary_name}"; then
            docker save ${var.source_image_url} | tar -xO "$layer" | tar -xO ${var.binary_name} | \
              gcloud storage cp - $path && \
              gcloud storage objects update $path --custom-metadata=goog-reserved-posix-mode=750
            exit 0
          fi
        done
        echo "Binary not found in image"
        exit 1
      fi
    EOT
  }
}
