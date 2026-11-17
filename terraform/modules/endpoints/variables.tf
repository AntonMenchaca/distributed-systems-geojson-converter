variable "prefix" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "route_table_ids" {
  description = "Route tables that get the endpoint routes"
  type        = list(string)
}
