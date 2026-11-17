variable "bucket_name" {
  description = "Globally unique bucket name, starting with the account prefix"
  type        = string
}

variable "region" {
  description = "Region the AWS CLI creates the bucket in"
  type        = string
}

variable "tags" {
  description = "Tags put on the bucket; provider default_tags don't reach a bucket the CLI creates"
  type        = map(string)
}
