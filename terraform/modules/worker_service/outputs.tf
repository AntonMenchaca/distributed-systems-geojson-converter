output "service_name" {
  value = module.service.service_name
}

output "task_definition_arn" {
  value = module.service.task_definition_arn
}

output "task_count" {
  value = var.task_count
}

output "cpu" {
  description = "CPU units per task (1024 = 1 vCPU)"
  value       = var.cpu
}

output "memory" {
  description = "MiB per task"
  value       = var.memory
}

output "ephemeral_storage_gb" {
  value = var.ephemeral_storage_gb
}

output "profile" {
  description = "PROFILE env value passed to the worker; 0 unless the profiling service is on"
  value       = lookup(var.environment, "PROFILE", "0")
}
