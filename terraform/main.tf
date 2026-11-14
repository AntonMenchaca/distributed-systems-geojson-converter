# The whole stack for one person's Learner Lab account: network, gateway endpoints, S3, SQS,
# DynamoDB, the API behind an ALB and the worker pool. Every experiment deploys this same
# stack and changes only knob variables:
#   terraform apply -var prefix=gc-c [-var lease_mode=fixed-12h ...]

data "aws_caller_identity" "current" {}

# Learner Lab cannot create IAM roles: every task role and execution role is LabRole.
data "aws_iam_role" "lab_role" {
  name = "LabRole"

  lifecycle {
    # Stop before touching anything if the credentials are not a Learner Lab session
    # (e.g. a work profile left in AWS_PROFILE).
    precondition {
      condition     = can(regex(":assumed-role/${var.learner_lab_role_name}/", data.aws_caller_identity.current.arn))
      error_message = "Not a Learner Lab session: caller is ${data.aws_caller_identity.current.arn}. Export AWS_PROFILE=<your Learner Lab profile> and refresh its credentials."
    }
  }
}

locals {
  # Every resource name goes through local.prefix, which reads the guarded LabRole lookup.
  # That makes the Learner Lab precondition a dependency of every resource, so even
  # `terraform apply -target=...` checks the caller first. (substr(..., 0, 0) is "".)
  prefix = "${var.prefix}${substr(data.aws_iam_role.lab_role.name, 0, 0)}"

  wiring = {
    AWS_REGION           = var.aws_region
    BUCKET               = module.s3.bucket
    JOBS_TABLE           = module.dynamodb.table_name
    QUEUE_URL_JOBS       = module.sqs.queue_urls["jobs"]
    QUEUE_URL_JOBS_SMALL = module.sqs.queue_urls["jobs-small"]
    QUEUE_URL_JOBS_LARGE = module.sqs.queue_urls["jobs-large"]
  }

  # Every knob, recorded by run.sh in each run's config.json (via `terraform output knobs`).
  knobs = {
    UPLOAD_MODE        = var.upload_mode
    QUEUE_LAYOUT       = var.queue_layout
    SCHED_POLICY       = var.sched_policy
    POOL_LAYOUT        = var.pool_layout
    LEASE_MODE         = var.lease_mode
    COMMIT_MODE        = var.commit_mode
    DLQ_MAX_RECEIVES   = tostring(var.dlq_max_receives)
    WORKER_CONCURRENCY = var.worker_concurrency
    SHUTDOWN_MODE      = var.shutdown_mode
    GDAL_PROFILE       = var.gdal_profile
    SHP_LIMIT_MODE     = var.shp_limit_mode
    ADMISSION          = var.admission
    SCALER             = var.scaler
    SCALER_COOLDOWN    = var.scaler_cooldown
    WORKER_IMAGE       = var.worker_image
    TASK_SHAPE         = "${var.worker_count}x${var.worker_cpu / 1024}-${var.worker_memory / 1024}-${var.worker_ephemeral_gb}gb"
    INPUT_MODE         = var.input_mode
    SPLIT_K            = tostring(var.split_k)
    SPECULATE          = var.speculate
  }
}

module "network" {
  source                = "./modules/network"
  name                  = local.prefix
  container_port        = var.container_port
  alb_security_group_id = module.alb.security_group_id
}

module "endpoints" {
  source          = "./modules/endpoints"
  prefix          = local.prefix
  vpc_id          = module.network.vpc_id
  route_table_ids = [module.network.public_route_table_id]
}

module "s3" {
  source = "./modules/s3"
  # Bucket names are global, so the account ID keeps each person's unique.
  bucket_name = "${local.prefix}-geojson-${data.aws_caller_identity.current.account_id}"
}

module "sqs" {
  source           = "./modules/sqs"
  prefix           = local.prefix
  dlq_max_receives = var.dlq_max_receives
}

module "dynamodb" {
  source = "./modules/dynamodb"
  prefix = local.prefix
}

module "alb" {
  source         = "./modules/alb"
  service_name   = "${local.prefix}-api"
  vpc_id         = module.network.vpc_id
  subnet_ids     = module.network.public_subnet_ids
  container_port = var.container_port
}

module "api_logs" {
  source            = "./modules/logging"
  service_name      = "${local.prefix}-api"
  retention_in_days = var.log_retention_days
}

module "worker_logs" {
  source            = "./modules/logging"
  service_name      = "${local.prefix}-worker"
  retention_in_days = var.log_retention_days
}

resource "aws_ecs_cluster" "this" {
  name = "${local.prefix}-cluster"

  # Container Insights publishes task counts and, with "enhanced", CPU and memory per task:
  # the source of resources.csv.
  setting {
    name  = "containerInsights"
    value = "enhanced"
  }
}

module "api" {
  source             = "./modules/ecs"
  service_name       = "${local.prefix}-api"
  cluster_id         = aws_ecs_cluster.this.id
  image              = "${module.api_ecr.repository_url}@${docker_registry_image.api.sha256_digest}"
  container_port     = var.container_port
  subnet_ids         = module.network.public_subnet_ids
  security_group_ids = [module.network.api_security_group_id]
  execution_role_arn = data.aws_iam_role.lab_role.arn
  task_role_arn      = data.aws_iam_role.lab_role.arn
  log_group_name     = module.api_logs.log_group_name
  ecs_count          = var.api_count
  cpu                = tostring(var.api_cpu)
  memory             = tostring(var.api_memory)
  region             = var.aws_region
  target_group_arn   = module.alb.target_group_arn

  environment = merge(local.wiring, {
    # Release mode stops Gin printing its debug banner between the JSON log lines.
    GIN_MODE     = "release"
    QUEUE_LAYOUT = var.queue_layout
    COMMIT_MODE  = var.commit_mode
  })

  # ECS can only register tasks in a target group that a listener already forwards to.
  depends_on = [module.alb]
}

module "worker" {
  source               = "./modules/worker_service"
  name                 = "${local.prefix}-worker"
  cluster_id           = aws_ecs_cluster.this.id
  image                = "${module.worker_ecr.repository_url}@${docker_registry_image.worker.sha256_digest}"
  subnet_ids           = module.network.public_subnet_ids
  security_group_ids   = [module.network.worker_security_group_id]
  role_arn             = data.aws_iam_role.lab_role.arn
  log_group_name       = module.worker_logs.log_group_name
  region               = var.aws_region
  task_count           = var.worker_count
  cpu                  = var.worker_cpu
  memory               = var.worker_memory
  ephemeral_storage_gb = var.worker_ephemeral_gb

  environment = merge(local.wiring, {
    WORKER_CONCURRENCY = var.worker_concurrency
    LEASE_MODE         = var.lease_mode
    COMMIT_MODE        = var.commit_mode
    GDAL_PROFILE       = var.gdal_profile
    WORK_DIR           = "/tmp/jobs"
  })
}
