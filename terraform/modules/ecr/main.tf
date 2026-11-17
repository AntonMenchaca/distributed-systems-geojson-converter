# One repository per image. force_delete lets `terraform destroy` remove it with its images,
# so a destroyed stack leaves nothing behind.
resource "aws_ecr_repository" "this" {
  name         = var.repository_name
  force_delete = true
}
