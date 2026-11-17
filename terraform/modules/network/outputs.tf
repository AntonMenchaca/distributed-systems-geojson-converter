output "vpc_id" {
  description = "ID of the VPC"
  value       = aws_vpc.this.id
}

output "public_subnet_ids" {
  description = "Public subnets, for the load balancer and every task"
  value       = aws_subnet.public[*].id
}

output "public_route_table_id" {
  description = "Route table the gateway endpoints attach to"
  value       = aws_route_table.public.id
}

output "api_security_group_id" {
  description = "Security group for API tasks"
  value       = aws_security_group.api.id
}

output "worker_security_group_id" {
  description = "Security group for worker tasks"
  value       = aws_security_group.worker.id
}
