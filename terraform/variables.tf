# ---------------------------------------------------------------------------
# Stack settings
# ---------------------------------------------------------------------------

variable "prefix" {
  description = "Owner of the Learner Lab account; every resource name starts with it and every resource is tagged with it"
  type        = string
  validation {
    condition     = contains(["gc-anton", "gc-ikwo", "gc-samuel"], var.prefix)
    error_message = "prefix must be gc-anton, gc-ikwo or gc-samuel."
  }
}

variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "learner_lab_role_name" {
  description = "Role an AWS Academy Learner Lab session assumes; plan fails for any other caller"
  type        = string
  default     = "voclabs"
}

variable "log_retention_days" {
  type    = number
  default = 3
}

variable "container_port" {
  type    = number
  default = 8080
}

variable "api_count" {
  type    = number
  default = 1
}

variable "api_cpu" {
  description = "API task CPU units (512 = 0.5 vCPU)"
  type        = number
  default     = 512
}

variable "api_memory" {
  description = "API task memory in MiB"
  type        = number
  default     = 1024
}

# ---------------------------------------------------------------------------
# Knobs (knobs.md). Each has two checks: the knob's full set of values, and the
# values the code supports today. The second check is widened by the step that
# builds the value, so a knob can never be set to something that silently does nothing.
# ---------------------------------------------------------------------------

variable "upload_mode" {
  type    = string
  default = "proxy-buffer"
  validation {
    condition     = contains(["proxy-buffer", "proxy-stream", "presigned"], var.upload_mode)
    error_message = "upload_mode: proxy-buffer, proxy-stream or presigned."
  }
  validation {
    condition     = var.upload_mode == "proxy-buffer"
    error_message = "upload_mode: only proxy-buffer is implemented so far."
  }
}

variable "queue_layout" {
  type    = string
  default = "single"
  validation {
    condition     = contains(["single", "by-size"], var.queue_layout)
    error_message = "queue_layout: single or by-size."
  }
}

variable "sched_policy" {
  type    = string
  default = "fcfs"
  validation {
    condition     = contains(["fcfs", "small-first", "weighted-3:1"], var.sched_policy)
    error_message = "sched_policy: fcfs, small-first or weighted-3:1."
  }
  validation {
    # small-first and weighted-3:1 choose between jobs-small and jobs-large, so they need
    # by-size queues read by one shared pool. fcfs reads exactly one queue.
    condition     = (var.sched_policy == "fcfs") == !(var.queue_layout == "by-size" && var.pool_layout == "shared")
    error_message = "sched_policy: fcfs reads one queue (queue_layout=single, or pool_layout=split); small-first and weighted-3:1 need queue_layout=by-size with pool_layout=shared."
  }
}

variable "pool_layout" {
  type    = string
  default = "shared"
  validation {
    condition     = contains(["shared", "split"], var.pool_layout)
    error_message = "pool_layout: shared or split."
  }
  validation {
    condition     = var.pool_layout == "shared" || var.queue_layout == "by-size"
    error_message = "pool_layout: split needs queue_layout=by-size, so each pool reads its own size queue."
  }
}

variable "lease_mode" {
  type    = string
  default = "fixed-30s"
  validation {
    condition     = contains(["fixed-30s", "fixed-12h", "heartbeat"], var.lease_mode)
    error_message = "lease_mode: fixed-30s, fixed-12h or heartbeat."
  }
  validation {
    condition     = var.lease_mode != "heartbeat"
    error_message = "lease_mode: heartbeat is not implemented yet."
  }
}

variable "commit_mode" {
  type    = string
  default = "blind"
  validation {
    condition     = contains(["blind", "fenced"], var.commit_mode)
    error_message = "commit_mode: blind or fenced."
  }
  validation {
    condition     = var.commit_mode == "blind"
    error_message = "commit_mode: fenced is not implemented yet."
  }
}

variable "dlq_max_receives" {
  description = "0 = no DLQ (baseline: poison jobs retry forever)"
  type        = number
  default     = 0
  validation {
    condition     = var.dlq_max_receives >= 0 && floor(var.dlq_max_receives) == var.dlq_max_receives
    error_message = "dlq_max_receives: a whole number >= 0."
  }
}

variable "worker_concurrency" {
  type    = string
  default = "fixed-4"
  validation {
    condition     = can(regex("^(fixed-[1-9][0-9]*|mem-budget)$", var.worker_concurrency))
    error_message = "worker_concurrency: fixed-<n> (n >= 1) or mem-budget."
  }
  validation {
    condition     = var.worker_concurrency != "mem-budget"
    error_message = "worker_concurrency: mem-budget is not implemented yet."
  }
}

variable "shutdown_mode" {
  type    = string
  default = "ignore"
  validation {
    condition     = contains(["ignore", "drain"], var.shutdown_mode)
    error_message = "shutdown_mode: ignore or drain."
  }
  validation {
    condition     = var.shutdown_mode == "ignore"
    error_message = "shutdown_mode: drain is not implemented yet."
  }
}

variable "gdal_profile" {
  type    = string
  default = "defaults"
  validation {
    condition     = contains(["defaults", "gt1", "tuned"], var.gdal_profile)
    error_message = "gdal_profile: defaults, gt1 or tuned."
  }
  validation {
    condition     = var.gdal_profile != "tuned"
    error_message = "gdal_profile: tuned is not implemented yet."
  }
}

variable "shp_limit_mode" {
  type    = string
  default = "none"
  validation {
    condition     = contains(["none", "split-2gb", "reject"], var.shp_limit_mode)
    error_message = "shp_limit_mode: none, split-2gb or reject."
  }
  validation {
    condition     = var.shp_limit_mode == "none"
    error_message = "shp_limit_mode: only none is implemented so far."
  }
}

variable "admission" {
  type    = string
  default = "off"
  validation {
    condition     = contains(["off", "validate", "backlog"], var.admission)
    error_message = "admission: off, validate or backlog."
  }
  validation {
    condition     = var.admission == "off"
    error_message = "admission: only off is implemented so far."
  }
}

variable "scaler" {
  type    = string
  default = "fixed"
  validation {
    condition     = contains(["fixed", "cpu", "memory", "backlog-msgs", "backlog-work", "native"], var.scaler)
    error_message = "scaler: fixed, cpu, memory, backlog-msgs, backlog-work or native."
  }
  validation {
    condition     = var.scaler == "fixed"
    error_message = "scaler: only fixed is implemented so far."
  }
}

variable "scaler_cooldown" {
  type    = string
  default = "none"
  validation {
    condition     = can(regex("^(none|out=[0-9]+,in=[0-9]+,band=[0-9.]+)$", var.scaler_cooldown))
    error_message = "scaler_cooldown: none or out=<s>,in=<s>,band=<fraction>."
  }
  validation {
    condition     = var.scaler_cooldown == "none"
    error_message = "scaler_cooldown: only none is implemented so far."
  }
}

variable "worker_image" {
  description = "WORKER_IMAGE: GDAL base of the worker image"
  type        = string
  default     = "gdal-full"
  validation {
    condition     = contains(["gdal-full", "gdal-slim"], var.worker_image)
    error_message = "worker_image: gdal-full or gdal-slim."
  }
}

# TASK_SHAPE: n x (vCPU, memory, disk). Baseline 4 x (1 vCPU, 4 GB, 20 GB).
variable "worker_count" {
  type    = number
  default = 4
  validation {
    condition     = var.worker_count >= 0 && floor(var.worker_count) == var.worker_count
    error_message = "worker_count: a whole number >= 0."
  }
}

# POOL_LAYOUT=split: the two pools' sizes. They must add up to worker_count, so a split run
# has the same total budget as the shared run it is compared with.
variable "worker_count_small" {
  description = "POOL_LAYOUT=split: tasks in the small pool (reads jobs-small)"
  type        = number
  default     = 2
  validation {
    condition     = var.pool_layout == "shared" || (var.worker_count_small >= 1 && var.worker_count_small + var.worker_count_large == var.worker_count)
    error_message = "worker_count_small: with pool_layout=split, each pool needs at least 1 task and worker_count_small + worker_count_large must equal worker_count (${var.worker_count})."
  }
}

variable "worker_count_large" {
  description = "POOL_LAYOUT=split: tasks in the large pool (reads jobs-large)"
  type        = number
  default     = 2
  validation {
    condition     = var.pool_layout == "shared" || var.worker_count_large >= 1
    error_message = "worker_count_large: with pool_layout=split, the large pool needs at least 1 task."
  }
}

variable "worker_cpu" {
  description = "CPU units per worker task (1024 = 1 vCPU)"
  type        = number
  default     = 1024
  validation {
    condition     = contains([256, 512, 1024, 2048, 4096, 8192, 16384], var.worker_cpu)
    error_message = "worker_cpu: a Fargate CPU size (256 ... 16384)."
  }
}

variable "worker_memory" {
  description = "MiB per worker task; must be a valid Fargate pairing with worker_cpu"
  type        = number
  default     = 4096
}

variable "worker_ephemeral_gb" {
  description = "GiB of disk per worker task; 20 is Fargate's default, 21-200 is configurable"
  type        = number
  default     = 20
  validation {
    condition     = var.worker_ephemeral_gb >= 20 && var.worker_ephemeral_gb <= 200
    error_message = "worker_ephemeral_gb: 20 to 200."
  }
}

variable "input_mode" {
  type    = string
  default = "download"
  validation {
    condition     = contains(["download", "stream"], var.input_mode)
    error_message = "input_mode: download or stream."
  }
  validation {
    condition     = var.input_mode == "download"
    error_message = "input_mode: stream is not implemented yet."
  }
}

variable "split_k" {
  type    = number
  default = 1
  validation {
    condition     = var.split_k == 1
    error_message = "split_k: only 1 is implemented so far."
  }
}

variable "speculate" {
  type    = string
  default = "off"
  validation {
    condition     = contains(["off", "on"], var.speculate)
    error_message = "speculate: off or on."
  }
  validation {
    condition     = var.speculate == "off"
    error_message = "speculate: only off is implemented so far."
  }
}
