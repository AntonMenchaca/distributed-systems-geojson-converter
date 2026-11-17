output "dns_name" {
  description = "Public DNS name of the load balancer"
  value       = aws_lb.this.dns_name
}

output "security_group_id" {
  description = "Security group of the load balancer"
  value       = aws_security_group.alb.id
}

output "target_group_arn" {
  description = "Target group the ECS service registers its tasks in"
  value       = aws_lb_target_group.this.arn
}

output "listener_arn" {
  description = "HTTP listener; the ECS service waits for it before registering tasks"
  value       = aws_lb_listener.http.arn
}

# CloudWatch identifies the load balancer and target group by these suffixes.
output "arn_suffix" {
  value = aws_lb.this.arn_suffix
}

output "target_group_arn_suffix" {
  value = aws_lb_target_group.this.arn_suffix
}
