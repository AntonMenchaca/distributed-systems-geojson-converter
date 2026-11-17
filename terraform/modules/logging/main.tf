# CloudWatch log group for one service's containers.
resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.service_name}"
  retention_in_days = var.retention_in_days
}
