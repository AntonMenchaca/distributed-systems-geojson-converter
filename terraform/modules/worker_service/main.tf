# The worker pool: an ECS service with no load balancer, sized by TASK_SHAPE, configured by
# the worker's env knobs. Pools for POOL_LAYOUT=split are more instances of this module.
module "service" {
  source = "../ecs"

  service_name         = var.name
  cluster_id           = var.cluster_id
  image                = var.image
  subnet_ids           = var.subnet_ids
  security_group_ids   = var.security_group_ids
  execution_role_arn   = var.role_arn
  task_role_arn        = var.role_arn
  log_group_name       = var.log_group_name
  region               = var.region
  ecs_count            = var.task_count
  cpu                  = tostring(var.cpu)
  memory               = tostring(var.memory)
  ephemeral_storage_gb = var.ephemeral_storage_gb
  environment          = var.environment
  stop_timeout_s       = var.stop_timeout_s
}
