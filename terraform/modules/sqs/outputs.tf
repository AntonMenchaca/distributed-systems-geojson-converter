output "queue_urls" {
  description = "Queue URL by logical name (jobs, jobs-small, jobs-large)"
  value       = { for k, q in aws_sqs_queue.this : k => q.url }
}

output "queue_arns" {
  value = { for k, q in aws_sqs_queue.this : k => q.arn }
}

output "dlq_urls" {
  description = "DLQ URL by logical name; empty when DLQ_MAX_RECEIVES = 0"
  value       = { for k, q in aws_sqs_queue.dlq : k => q.url }
}
