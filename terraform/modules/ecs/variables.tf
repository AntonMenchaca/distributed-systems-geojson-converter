variable "service_name" {
  type        = string
  description = "Base name for ECS resources"
}

variable "image" {
  type        = string
  description = "ECR image URI (with tag)"
}

variable "container_port" {
  type        = number
  default     = null
  description = "Port the app listens on; null for a service that listens on nothing (the worker)"
}

variable "cluster_id" {
  type        = string
  description = "ECS cluster the service runs in"
}

variable "assign_public_ip" {
  type        = bool
  default     = true
  description = "Give tasks a public IP (needed in public subnets without a NAT gateway)"
}

variable "ephemeral_storage_gb" {
  type        = number
  default     = 20
  description = "Task disk in GiB; 20 is Fargate's default, 21-200 is configurable"
}

variable "subnet_ids" {
  type        = list(string)
  description = "Subnets for FARGATE tasks"
}

variable "security_group_ids" {
  type        = list(string)
  description = "SGs for FARGATE tasks"
}

variable "execution_role_arn" {
  type        = string
  description = "ECS Task Execution Role ARN"
}

variable "task_role_arn" {
  type        = string
  description = "IAM Role ARN for app permissions"
}

variable "log_group_name" {
  type        = string
  description = "CloudWatch log group name"
}

variable "ecs_count" {
  type        = number
  default     = 1
  description = "Desired Fargate task count"
}

variable "region" {
  type        = string
  description = "AWS region (for awslogs driver)"
}

variable "cpu" {
  type        = string
  default     = "256"
  description = "vCPU units"
}

variable "memory" {
  type        = string
  default     = "512"
  description = "Memory (MiB)"
}

variable "target_group_arn" {
  type        = string
  default     = null
  description = "Load balancer target group to register tasks in; null for a service with no load balancer"
}

variable "health_check_path" {
  type        = string
  default     = null
  description = "Path ECS calls inside the container to check its health, such as /health; null for no container health check"
}

variable "environment" {
  type        = map(string)
  default     = {}
  description = "Environment variables for the container, such as PAYMENT_SLOTS"
}

variable "stop_timeout_s" {
  type        = number
  default     = null
  description = "ECS stopTimeout in seconds, from SIGTERM to SIGKILL (Fargate: 2-120); null leaves the default (30 s)"
}
