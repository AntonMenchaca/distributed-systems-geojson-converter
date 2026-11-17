output "service_name" {
  description = "ECS service name"
  value       = aws_ecs_service.this.name
}

output "task_definition_arn" {
  description = "Task definition the service runs, recorded in each run's config.json"
  value       = aws_ecs_task_definition.this.arn
}
