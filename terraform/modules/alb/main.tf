# Application Load Balancer in front of the ECS tasks. It listens on port
# 80 and forwards to whichever tasks the target group reports as healthy.

# The load balancer accepts HTTP from anywhere.
resource "aws_security_group" "alb" {
  name        = "${var.service_name}-alb-sg"
  description = "Allow inbound HTTP to the load balancer"
  vpc_id      = var.vpc_id

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
    description = "Allow HTTP traffic"
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
    description = "Allow all outbound"
  }
}

resource "aws_lb" "this" {
  name               = "${var.service_name}-alb"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.subnet_ids
}

# Fargate tasks have no instance of their own, so they register by IP.
resource "aws_lb_target_group" "this" {
  name        = "${var.service_name}-tg"
  target_type = "ip"
  protocol    = "HTTP"
  port        = var.container_port
  vpc_id      = var.vpc_id

  # The default is 300 seconds, which holds every scale-in and redeploy for
  # 5 minutes while a removed task drains. Requests here take milliseconds.
  deregistration_delay = 30

  health_check {
    path                = "/health"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 2
    timeout             = 5
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}
