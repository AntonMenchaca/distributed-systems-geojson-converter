terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.7.0"
    }
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 2.25"
    }
  }
}

# Credentials come from your Learner Lab session: export AWS_PROFILE=<your Learner Lab
# profile> first. data.aws_iam_role.lab_role (main.tf) refuses any other identity.
provider "aws" {
  region = var.aws_region

  # Every resource carries the account prefix, so a teardown check can find leftovers with
  # `aws resourcegroupstaggingapi get-resources --tag-filters Key=Prefix,Values=<prefix>`.
  default_tags {
    tags = {
      Project = "geojson-converter"
      Prefix  = var.prefix
    }
  }
}

# Terraform's Docker provider logs in to ECR with a short-lived token.
data "aws_ecr_authorization_token" "registry" {}

provider "docker" {
  registry_auth {
    address  = data.aws_ecr_authorization_token.registry.proxy_endpoint
    username = data.aws_ecr_authorization_token.registry.user_name
    password = data.aws_ecr_authorization_token.registry.password
  }
}
