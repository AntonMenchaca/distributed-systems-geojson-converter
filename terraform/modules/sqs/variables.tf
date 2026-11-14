variable "prefix" {
  description = "Account prefix for queue names"
  type        = string
}

variable "dlq_max_receives" {
  description = "DLQ_MAX_RECEIVES knob: receives before a message moves to its DLQ; 0 = no DLQ"
  type        = number
}
