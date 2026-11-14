# Build the API and worker images for linux/amd64 (Fargate) and push them to ECR. Services
# reference them by digest, so a code change rolls out new tasks; a source hash triggers the
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
  }

  src_hash = { for name, globs in {
    api    = ["api/**", "jobstore/**", "config/**", "datasets/local/**"]
    worker = ["worker/cmd/**", "worker/internal/**", "worker/go.*", "worker/Dockerfile", "jobstore/**"]
    } : name => sha1(join("", [
      for f in sort(flatten([for g in globs : tolist(fileset(local.repo_root, g))])) : filesha1("${local.repo_root}/${f}")
  ])) }
}

resource "docker_image" "api" {
  name = "${module.api_ecr.repository_url}:latest"
  build {
    context    = local.repo_root
    dockerfile = "api/Dockerfile"
    platform   = "linux/amd64"
  }
  triggers = { src = local.src_hash["api"] }
}

resource "docker_registry_image" "api" {
  name     = docker_image.api.name
  triggers = { src = local.src_hash["api"] }
}

resource "docker_image" "worker" {
  name = "${module.worker_ecr.repository_url}:latest"
  build {
    context    = local.repo_root
    dockerfile = "worker/Dockerfile"
    platform   = "linux/amd64"
    build_arg = {
      GDAL_IMAGE = local.gdal_images[var.worker_image]
    }
  }
  triggers = { src = "${local.src_hash["worker"]}-${var.worker_image}" }
}

resource "docker_registry_image" "worker" {
  name     = docker_image.worker.name
  triggers = { src = "${local.src_hash["worker"]}-${var.worker_image}" }
}
