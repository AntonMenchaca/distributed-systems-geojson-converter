# Build the API and worker images for linux/amd64 (Fargate) and push them to ECR with the
# Docker CLI (via local-exec), then look up the pushed digests. Services reference the
# images by digest, so a code change rolls out new tasks; a source hash triggers the
# rebuild, because the image name alone never changes.

module "api_ecr" {
  source          = "./modules/ecr"
  repository_name = "${local.prefix}-api"
}

module "worker_ecr" {
  source          = "./modules/ecr"
  repository_name = "${local.prefix}-worker"
}

locals {
  repo_root = "${path.module}/.."

  # WORKER_IMAGE knob: the GDAL base the worker image is built on, pinned by digest.
  gdal_images = {
    gdal-full = "ghcr.io/osgeo/gdal:ubuntu-full-3.13.3@sha256:2dd0f81ef927ff4c3d4dbe4f73c029dc86d4073974c0564f8e196f6e1412e2e0"
    gdal-slim = "ghcr.io/osgeo/gdal:ubuntu-small-3.13.3@sha256:64250faf833c06d4b21afce4c27190039ba7ab58d70f0eebc87cf77d929c0b40"
    gdal-small = "ghcr.io/osgeo/gdal:alpine-small-3.13.0@sha256:dc253a8795f30917f72e3a135cea1613a18d8d19eab8d9f9ce99f6353b1d69a5"
  }

  src_hash = { for name, globs in {
    api    = ["api/**", "jobstore/**", "config/**", "datasets/local/**"]
    worker = ["worker/cmd/**", "worker/internal/**", "worker/go.*", "worker/Dockerfile", "jobstore/**"]
    } : name => sha1(join("", [
      for f in sort(flatten([for g in globs : tolist(fileset(local.repo_root, g))])) : filesha1("${local.repo_root}/${f}")
  ])) }

  ecr_registry = split("/", module.api_ecr.repository_url)[0]
  ecr_region   = split(".", local.ecr_registry)[3]
}

resource "terraform_data" "api_image" {
  triggers_replace = local.src_hash["api"]

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -e
      aws ecr get-login-password --region ${local.ecr_region} | docker login --username AWS --password-stdin ${local.ecr_registry}
      docker build --platform linux/amd64 -f ${local.repo_root}/api/Dockerfile -t ${module.api_ecr.repository_url}:latest ${local.repo_root}
      docker push ${module.api_ecr.repository_url}:latest
    EOT
  }
}

resource "terraform_data" "worker_image" {
  triggers_replace = "${local.src_hash["worker"]}-${var.worker_image}"

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = <<-EOT
      set -e
      aws ecr get-login-password --region ${local.ecr_region} | docker login --username AWS --password-stdin ${local.ecr_registry}
      docker build --platform linux/amd64 -f ${local.repo_root}/worker/Dockerfile --build-arg GDAL_IMAGE=${local.gdal_images[var.worker_image]} -t ${module.worker_ecr.repository_url}:latest ${local.repo_root}
      docker push ${module.worker_ecr.repository_url}:latest
    EOT
  }
}

data "aws_ecr_image" "api" {
  repository_name = split("/", module.api_ecr.repository_url)[1]
  image_tag       = "latest"
  depends_on      = [terraform_data.api_image]
}

data "aws_ecr_image" "worker" {
  repository_name = split("/", module.worker_ecr.repository_url)[1]
  image_tag       = "latest"
  depends_on      = [terraform_data.worker_image]
}