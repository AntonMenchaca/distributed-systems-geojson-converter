# Gateway endpoints for S3 and DynamoDB. They are free, and keep the bulk traffic (GeoJSON in,
# outputs out, job-record writes) on the AWS network instead of the internet gateway.
data "aws_region" "current" {}

resource "aws_vpc_endpoint" "gateway" {
  for_each          = toset(["s3", "dynamodb"])
  vpc_id            = var.vpc_id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.${each.key}"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = var.route_table_ids

  tags = { Name = "${var.prefix}-${each.key}-endpoint" }
}
