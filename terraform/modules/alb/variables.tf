variable "service_name" {
  type        = string
  description = "Prefix for the load balancer, target group and security group names"
}

variable "vpc_id" {
  type        = string
  description = "VPC the load balancer and targets run in"
}

variable "subnet_ids" {
  type        = list(string)
  description = "Subnets for the load balancer, in at least two availability zones"
}

variable "container_port" {
  type        = number
  description = "Port the tasks listen on"
}
