output "endpoint_ids" {
  value = { for k, e in aws_vpc_endpoint.gateway : k => e.id }
}
