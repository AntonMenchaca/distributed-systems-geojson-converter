# Offline plan tests with mocked providers: `terraform test` (no AWS credentials needed).

mock_provider "aws" {
  # Mocked strings are not ARNs; the ECS task definition validates role ARNs.
  mock_data "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/LabRole" }
  }
}

override_data {
  target = module.network.data.aws_availability_zones.available
  values = { names = ["us-east-1a", "us-east-1b"] }
}

mock_provider "docker" {}

variables {
  prefix = "gc-c"
}

run "learner_lab_session_plans" {
  command = plan
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/voclabs/user1234567=Student" }
  }
  assert {
    condition     = output.knobs["TASK_SHAPE"] == "4x1-4-20gb" && output.knobs["LEASE_MODE"] == "fixed-30s"
    error_message = "baseline knobs changed: ${jsonencode(output.knobs)}"
  }
}

run "non_learner_lab_caller_is_refused" {
  command = plan
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:user/someone" }
  }
  expect_failures = [data.aws_iam_role.lab_role]
}

run "unbuilt_knob_value_is_refused" {
  command = plan
  variables {
    lease_mode = "heartbeat"
  }
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/voclabs/user1234567=Student" }
  }
  expect_failures = [var.lease_mode]
}

run "unknown_prefix_is_refused" {
  command = plan
  variables {
    prefix = "prod"
  }
  expect_failures = [var.prefix]
}

# -target plans only a resource and its dependencies, so the guard must be a dependency of
# every resource, not only of the ECS services.
run "targeted_plan_by_non_learner_lab_caller_is_refused" {
  command = plan
  plan_options {
    target = [module.s3, docker_registry_image.worker, module.sqs]
  }
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:user/someone" }
  }
  expect_failures = [data.aws_iam_role.lab_role]
}
