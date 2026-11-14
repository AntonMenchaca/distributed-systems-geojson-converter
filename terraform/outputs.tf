output "alb_url" {
  description = "Send requests here; the load balancer forwards them to the API tasks"
  value       = "http://${module.alb.dns_name}"
}

output "prefix" {
  value = var.prefix
}

output "account_id" {
  value = data.aws_caller_identity.current.account_id
}

output "bucket" {
  value = module.s3.bucket
}

output "jobs_table" {
  value = module.dynamodb.table_name
}

output "queue_urls" {
  value = module.sqs.queue_urls
}

output "dlq_urls" {
  value = module.sqs.dlq_urls
}

output "cluster_name" {
  value = aws_ecs_cluster.this.name
}

output "api_service_name" {
  value = module.api.service_name
}

output "worker_service_name" {
  value = module.worker.service_name
}

output "task_definitions" {
  description = "Recorded in each run's config.json as task_defs"
  value = {
    api    = module.api.task_definition_arn
    worker = module.worker.task_definition_arn
  }
}

output "knobs" {
  description = "Every knob's value for this deployment; run.sh copies it into config.json"
  value       = local.knobs
}
