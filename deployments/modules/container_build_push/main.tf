# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

locals {
  # Add .git suffix if it's a GitHub URL and doesn't already end with .git
  repo = (
    can(regex("^https://github\\.com/", var.repo_url)) && !endswith(var.repo_url, ".git") ? "${var.repo_url}.git" : var.repo_url
  )

  repo_is_local = startswith(local.repo, "file://")

  git_dir = (
    !local.repo_is_local ? "!remote!" :
    fileexists(join("/", [substr(local.repo, 7, -1), ".git/config"])) ? join("/", [substr(local.repo, 7, -1), ".git"]) :
    fileexists(join("/", [substr(local.repo, 7, -1), ".jj/repo/store/git/config"])) ? join("/", [substr(local.repo, 7, -1), ".jj/repo/store/git"]) :
  "")

  repo_docker_context = (
    local.repo_is_local
    ? "- < <(GIT_DIR=${local.git_dir} git archive --format=tar ${var.commit})"
  : "${local.repo}#${var.commit}")

  full_image_url = "${var.image_url}:${var.image_version}"

  build_args_str = join(" ", [for arg in var.build_args : "--build-arg ${arg}"])
}

resource "terraform_data" "image_deps" {
  input = {
    name          = var.name
    image_url     = var.image_url
    image_version = var.image_version
    repo          = local.repo
    commit        = var.commit,
    # NOTE: Exclude var.build_args and var.dockerfile_path since they don't affect the image URL.
  }
}

resource "terraform_data" "image" {
  input = terraform_data.image_deps.output

  lifecycle {
    replace_triggered_by =  [terraform_data.image_deps]
  }

  provisioner "local-exec" {
    command = <<-EOT
      path=${local.full_image_url}
      cmd="gcloud artifacts docker images describe $path"
      # Suppress stdout, show first line of stderr, return cmd's status.
      if ($cmd 2>&1 1>/dev/null | head -n1 >&2; exit $PIPESTATUS); then
        echo Found $path
      else
        echo Building $path
        docker build --quiet ${local.build_args_str} -f ${var.dockerfile_path} -t $path ${local.repo_docker_context} && \
          docker push --quiet $path
      fi
    EOT
  }
}
