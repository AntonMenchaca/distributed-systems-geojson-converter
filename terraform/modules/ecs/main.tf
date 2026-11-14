# The cluster is created once in the root module and shared by the API and worker services.

# Task Definition
resource "aws_ecs_task_definition" "this" {
  family                   = "${var.service_name}-task"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.cpu
  memory                   = var.memory

  execution_role_arn = var.execution_role_arn
  task_role_arn      = var.task_role_arn

  # Fargate gives 20 GiB by default; asking for more (21-200) is part of TASK_SHAPE.
  dynamic "ephemeral_storage" {
    for_each = var.ephemeral_storage_gb > 20 ? [var.ephemeral_storage_gb] : []
    content {
      size_in_gib = ephemeral_storage.value
    }
  }

  container_definitions = jsonencode([merge({
    name      = "${var.service_name}-container"
    image     = var.image
    essential = true

    # A worker listens on nothing, so it has no port mapping.
    portMappings = var.container_port == null ? [] : [{
      containerPort = var.container_port
    }]

    environment = [for name, value in var.environment : { name = name, value = value }]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = var.log_group_name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "ecs"
      }
    }
    },
    # Without a load balancer checking the task, ECS can call the
    # container's health endpoint itself. The golang base image has curl.
    # The key is left out entirely when there's no path, so the receiver's
    # task definition doesn't change.
    { for key, check in {
      healthCheck = {
        command     = ["CMD-SHELL", "curl -fs http://localhost:${coalesce(var.container_port, 0)}${coalesce(var.health_check_path, "/")} || exit 1"]
        interval    = 30
        timeout     = 5
        retries     = 3
        startPeriod = 10
      }
    } : key => check if var.health_check_path != null }
  )])
}

# ECS Service
resource "aws_ecs_service" "this" {
  name            = var.service_name
  cluster         = var.cluster_id
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.ecs_count
  launch_type     = "FARGATE"

  # New tasks get this long to start and pass the load balancer's health
  # checks before ECS replaces them for failing.
  health_check_grace_period_seconds = var.target_group_arn == null ? null : 60

  # A service such as the order processor that only reads a queue has no
  # load balancer.
  dynamic "load_balancer" {
    for_each = var.target_group_arn == null ? [] : [var.target_group_arn]
    content {
      target_group_arn = load_balancer.value
      container_name   = "${var.service_name}-container"
      container_port   = var.container_port
    }
  }

  network_configuration {
    subnets         = var.subnet_ids
    security_groups = var.security_group_ids
    # No NAT gateway (modules/network): tasks in public subnets need a public IP to reach
    # ECR, CloudWatch Logs and SQS.
    assign_public_ip = var.assign_public_ip
  }
}
