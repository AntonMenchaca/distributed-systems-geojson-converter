variable "name" {
  description = "Prefix for resource names (the account prefix, e.g. gc-c)"
  type        = string
}

variable "container_port" {
  description = "Port the API tasks listen on"
  type        = number
}

variable "alb_security_group_id" {
  description = "Security group of the load balancer, the only source allowed into API tasks"
  type        = string
}

variable "vpc_cidr" {
  description = "Address range of the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "Public subnets, one per availability zone"
  type        = list(string)
  default     = ["10.0.1.0/24", "10.0.2.0/24"]
}
