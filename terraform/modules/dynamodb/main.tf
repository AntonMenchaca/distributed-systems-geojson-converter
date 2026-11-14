# The jobs table. Same key, index and projection as jobstore.CreateTableInput and
# local/init.sh: tools/collect.py exports one run through run_id-index.
resource "aws_dynamodb_table" "jobs" {
  name         = "${var.prefix}-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "job_id"

  attribute {
    name = "job_id"
    type = "S"
  }

  attribute {
    name = "run_id"
    type = "S"
  }

  global_secondary_index {
    name            = "run_id-index"
    hash_key        = "run_id"
    projection_type = "ALL"
  }
}
