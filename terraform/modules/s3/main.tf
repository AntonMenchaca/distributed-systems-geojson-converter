# The run bucket: datasets/ (pre-staged inputs), uploads/, outputs/{job_id}/..., and tmp/.
# force_destroy lets `terraform destroy` empty and delete it in the same sitting.
resource "aws_s3_bucket" "this" {
  bucket        = var.bucket_name
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "this" {
  bucket                  = aws_s3_bucket.this.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.id

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
