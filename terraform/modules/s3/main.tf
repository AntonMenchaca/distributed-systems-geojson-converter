# The run bucket: datasets/ (pre-staged inputs), uploads/, outputs/{job_id}/..., and tmp/.
#
# Learner Lab's SCP denies s3:GetBucketObjectLockConfiguration, and aws_s3_bucket reads it after
# every create and refresh, so the bucket itself is created and deleted with the AWS CLI (same
# AWS_PROFILE as Terraform). Its settings below are separate resources whose reads are allowed.
# Provider default_tags don't reach a CLI-made bucket, so it is tagged here for the teardown check.
resource "terraform_data" "bucket" {
  triggers_replace = var.bucket_name
  input = {
    name    = var.bucket_name
    region  = var.region
    tagging = "TagSet=[${join(",", [for k, v in var.tags : "{Key=${k},Value=${v}}"])}]"
  }

  # head-bucket first, so re-running apply adopts a bucket an earlier failed apply created.
  provisioner "local-exec" {
    command = <<-EOT
      set -e
      if ! aws s3api head-bucket --bucket "$BUCKET" >/dev/null 2>&1; then
        if [ "$AWS_REGION" = us-east-1 ]; then
          aws s3api create-bucket --bucket "$BUCKET" >/dev/null
        else
          aws s3api create-bucket --bucket "$BUCKET" --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null
        fi
      fi
      aws s3api put-bucket-tagging --bucket "$BUCKET" --tagging "$TAGGING"
    EOT
    environment = {
      BUCKET     = self.input.name
      AWS_REGION = self.input.region
      TAGGING    = self.input.tagging
    }
  }

  # Empties and deletes the bucket, as force_destroy would, so `terraform destroy` finishes in one sitting.
  provisioner "local-exec" {
    when    = destroy
    command = "aws s3 rb \"s3://$BUCKET\" --force"
    environment = {
      BUCKET     = self.input.name
      AWS_REGION = self.input.region
    }
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  bucket                  = terraform_data.bucket.output.name
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = terraform_data.bucket.output.name

  rule {
    id     = "tmp-1-day"
    status = "Enabled"
    filter {
      prefix = "tmp/"
    }
    expiration {
      days = 1
    }
  }
}
