# POOL_LAYOUT and SCHED_POLICY: offline plan tests with mocked providers (`terraform test`).

mock_provider "aws" {
  mock_data "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/LabRole" }
  }
}

override_data {
  target = module.network.data.aws_availability_zones.available
  values = { names = ["us-east-1a", "us-east-1b"] }
}

override_data {
  target = data.aws_caller_identity.current
  values = { account_id = "123456789012", arn = "arn:aws:sts::123456789012:assumed-role/voclabs/user1234567=Student" }
}

variables {
  prefix = "gc-ikwo"
}

run "shared_pool_plans_one_worker_service" {
  command = plan
  assert {
    condition     = length(output.worker_service_names) == 1 && contains(keys(output.worker_service_names), "worker")
    error_message = "shared pool: ${jsonencode(keys(output.worker_service_names))}"
  }
}

# 4.2's acceptance criterion: POOL_LAYOUT=split plans two worker services.
run "split_pool_plans_two_worker_services" {
  command = plan
  variables {
    queue_layout = "by-size"
    pool_layout  = "split"
  }
  assert {
    condition     = length(output.worker_service_names) == 2 && contains(keys(output.worker_service_names), "worker-small") && contains(keys(output.worker_service_names), "worker-large")
    error_message = "split pools: ${jsonencode(keys(output.worker_service_names))}"
  }
}

run "small_first_plans_with_by_size_queues" {
  command = plan
  variables {
    queue_layout = "by-size"
    sched_policy = "small-first"
  }
  assert {
    condition     = output.knobs["SCHED_POLICY"] == "small-first" && length(output.worker_service_names) == 1
    error_message = "small-first: ${jsonencode(output.knobs)}"
  }
}

run "split_needs_by_size_queues" {
  command = plan
  variables {
    pool_layout = "split"
  }
  expect_failures = [var.pool_layout]
}

run "split_keeps_the_total_budget" {
  command = plan
  variables {
    queue_layout       = "by-size"
    pool_layout        = "split"
    worker_count_small = 3
  }
  expect_failures = [var.worker_count_small]
}

run "small_first_needs_by_size_queues" {
  command = plan
  variables {
    sched_policy = "small-first"
  }
  expect_failures = [var.sched_policy]
}

run "fcfs_with_two_queues_is_refused" {
  command = plan
  variables {
    queue_layout = "by-size"
  }
  expect_failures = [var.sched_policy]
}
