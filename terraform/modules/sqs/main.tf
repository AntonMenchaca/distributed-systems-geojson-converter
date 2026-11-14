# The job queues: jobs (QUEUE_LAYOUT=single), jobs-small and jobs-large (by-size). All three
# always exist; an idle queue costs nothing. Each gets a dead-letter queue only when
# DLQ_MAX_RECEIVES > 0; the baseline (0) lets poison jobs return forever.
locals {
  queues  = toset(["jobs", "jobs-small", "jobs-large"])
  use_dlq = var.dlq_max_receives > 0
}

resource "aws_sqs_queue" "dlq" {
  for_each                  = local.use_dlq ? local.queues : toset([])
  name                      = "${var.prefix}-${each.key}-dlq"
  message_retention_seconds = 1209600 # 14 days, the maximum, so dead letters outlive the run
}

resource "aws_sqs_queue" "this" {
  for_each = local.queues
  name     = "${var.prefix}-${each.key}"
  # SQS's default visibility: LEASE_MODE=fixed-30s. fixed-12h and heartbeat change it per
  # message from the worker, so the queue default stays the naive value.
  visibility_timeout_seconds = 30
  receive_wait_time_seconds  = 20

  redrive_policy = local.use_dlq ? jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = var.dlq_max_receives
  }) : null
}
