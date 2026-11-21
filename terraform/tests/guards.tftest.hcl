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

variables {
  prefix = "gc-anton"
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
  assert {
    condition     = module.worker.task_count == 4 && module.worker.cpu == 1024 && module.worker.memory == 4096 && module.worker.ephemeral_storage_gb == 20 && module.worker.profile == "0"
    error_message = "baseline worker is not 4 x (1 vCPU, 4 GB, 20 GB) with PROFILE off"
  }
}

run "profiling_service_is_one_large_task" {
  command = plan

  variables {
    prefix    = "gc-samuel"
    profiling = true
  }

  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/voclabs/user1234567=Student" }
  }

  assert {
    condition     = module.worker.task_count == 1 && module.worker.cpu == 4096 && module.worker.memory == 16384 && module.worker.ephemeral_storage_gb == 100 && module.worker.profile == "1"
    error_message = "profiling service is ${module.worker.task_count} tasks, ${module.worker.cpu} CPU units, ${module.worker.memory} MiB, ${module.worker.ephemeral_storage_gb} GiB, PROFILE=${module.worker.profile}; want 1 x 4096, 16384, 100, PROFILE=1"
  }
  assert {
    condition     = output.knobs["TASK_SHAPE"] == "1x4-16-100gb"
    error_message = "recorded TASK_SHAPE is ${output.knobs["TASK_SHAPE"]}, want 1x4-16-100gb"
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

run "letter_prefix_is_refused" {
  command = plan
  variables {
    prefix = "gc-c"
  }
  expect_failures = [var.prefix]
}

# gc-samuel is the longest prefix, so it is the one that could push a name past an AWS limit
# (load balancer and target group names allow 32 characters).
run "longest_owner_prefix_plans" {
  command = plan
  variables {
    prefix = "gc-samuel"
  }
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/voclabs/user1234567=Student" }
  }
}

# -target plans only a resource and its dependencies, so the guard must be a dependency of
# every resource, not only of the ECS services.
run "targeted_plan_by_non_learner_lab_caller_is_refused" {
  command = plan
  plan_options {
    target = [module.s3, terraform_data.worker_image, module.sqs]
  }
  override_data {
    target = data.aws_caller_identity.current
    values = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:user/someone" }
  }
  expect_failures = [data.aws_iam_role.lab_role]
}
