variable "name" {
  description = "Service name, starting with the account prefix (e.g. gc-c-worker)"
  type        = string
}

variable "cluster_id" {
  type = string
}

variable "image" {
  description = "Worker image URI, pinned by digest"
  type        = string
}

variable "subnet_ids" {
  type = list(string)
}

variable "security_group_ids" {
  type = list(string)
}

variable "role_arn" {
  description = "Task and execution role: always LabRole in Learner Lab"
  type        = string
}

variable "log_group_name" {
  type = string
}

variable "region" {
  type = string
}

variable "task_count" {
  description = "TASK_SHAPE: number of tasks"
  type        = number
}

variable "cpu" {
  description = "TASK_SHAPE: CPU units per task (1024 = 1 vCPU)"
  type        = number
}

variable "memory" {
  description = "TASK_SHAPE: MiB per task"
  type        = number
}

variable "ephemeral_storage_gb" {
  description = "TASK_SHAPE: disk per task in GiB"
  type        = number
}

variable "environment" {
  description = "Worker env: wiring plus env knobs"
  type        = map(string)
}

variable "stop_timeout_s" {
  description = "STOP_TIMEOUT_S knob: ECS stopTimeout for the worker container, in seconds"
  type        = number
}
