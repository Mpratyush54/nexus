# ecs.tf — ECS/Fargate deployment for the central server (issue #20).
#
# Topology: public ALB -> server tasks (awsvpc, private subnets). Secrets are
# injected via the `secrets` block (valueFrom = secret ARN + json key), so
# DATABASE_URL/JWT material never appears in task definitions, logs, or git.
# CPU/memory: 512/1024 is the smallest sane Fargate pairing for a Go
# REST+WebSocket API (256/512 would OOM under WS fan-out + pgx pool).
#
# NOTE: image URI is a variable because ECR push happens in CI (follow-up).
# NOTE: `terraform validate` passes without AWS credentials; apply was NOT run
# (no creds in this environment — see docs/issues/ISSUE-20.md verification).

resource "aws_security_group" "alb" {
  name        = "${var.project}-alb"
  description = "Public HTTPS ingress for the central-memory ALB (issue #20)."
  vpc_id      = local.effective_vpc_id
  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    description = "HTTP for the :80 -> :443 redirect listener only (issue #45); nothing is served on port 80."
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "server" {
  name        = "${var.project}-server"
  description = "Server tasks: accept ALB traffic only (issue #20)."
  vpc_id      = local.effective_vpc_id
  ingress {
    description     = "ALB to server"
    from_port       = var.server_port
    to_port         = var.server_port
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_lb" "server" {
  name               = "${var.project}-alb"
  load_balancer_type = "application"
  # Internet-facing: must sit on PUBLIC subnets (issue #45 — private
  # subnets here would blackhole all traffic). Tasks stay on the private
  # effective_subnets via the service network_configuration below.
  internal           = false
  subnets            = local.effective_public_subnets
  security_groups    = [aws_security_group.alb.id]
}

resource "aws_lb_target_group" "server" {
  name        = "${var.project}-srv"
  port        = var.server_port
  protocol    = "HTTP"
  vpc_id      = local.effective_vpc_id
  target_type = "ip"
  health_check {
    path                = "/healthz"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 15
    timeout             = 5
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.server.arn
  port              = 443
  protocol          = "HTTPS"
  # Placeholder cert: replace with a real ACM ARN at apply time (variable in
  # a follow-up; hardcoded ARNs are banned by the no-hardcode rule, so this
  # resolve-at-apply data source keeps the stack generic).
  certificate_arn = data.aws_acm_certificate.api.arn
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.server.arn
  }
}

# HTTP :80 exists ONLY to redirect to HTTPS (issue #45): plain-HTTP API
# traffic must never be served, and HSTS-less clients get bounced, not 404s.
resource "aws_lb_listener" "http_redirect" {
  load_balancer_arn = aws_lb.server.arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

data "aws_acm_certificate" "api" {
  domain   = var.api_domain
  statuses = ["ISSUED"]
}

resource "aws_ecs_cluster" "main" {
  name = "${var.project}-cluster"
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_cloudwatch_log_group" "server" {
  name              = "/ecs/${var.project}-server"
  retention_in_days = 30
}

resource "aws_iam_role" "task_exec" {
  name = "${var.project}-task-exec"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "task_exec_base" {
  role       = aws_iam_role.task_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Least-privilege read of exactly the two issue-#20 secrets.
resource "aws_iam_role_policy" "task_exec_secrets" {
  name = "${var.project}-read-secrets"
  role = aws_iam_role.task_exec.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = ["secretsmanager:GetSecretValue"]
      Resource = [
        aws_secretsmanager_secret.db_app.arn,
        aws_secretsmanager_secret.jwt.arn,
      ]
    }]
  })
}

resource "aws_ecs_task_definition" "server" {
  family                   = "${var.project}-server"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.server_cpu)
  memory                   = tostring(var.server_memory)
  execution_role_arn       = aws_iam_role.task_exec.arn

  container_definitions = jsonencode([{
    name      = "server"
    image     = var.server_image
    essential = true
    portMappings = [{
      containerPort = var.server_port
      protocol      = "tcp"
    }]
    environment = [
      { name = "PORT", value = tostring(var.server_port) },
      { name = "DB_SSLMODE", value = "require" },
      { name = "MIGRATIONS_DIR", value = "/app/migrations" },
    ]
    # Secrets Manager refs — resolved by ECS at launch, never in git/env files.
    secrets = [
      { name = "DB_HOST", valueFrom = "${aws_secretsmanager_secret.db_app.arn}:host::" },
      { name = "DB_PORT", valueFrom = "${aws_secretsmanager_secret.db_app.arn}:port::" },
      { name = "DB_NAME", valueFrom = "${aws_secretsmanager_secret.db_app.arn}:dbname::" },
      { name = "DB_USER", valueFrom = "${aws_secretsmanager_secret.db_app.arn}:username::" },
      { name = "DB_PASSWORD", valueFrom = "${aws_secretsmanager_secret.db_app.arn}:password::" },
      { name = "JWT_SECRET", valueFrom = "${aws_secretsmanager_secret.jwt.arn}:signing_key::" },
    ]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.server.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "server"
      }
    }
    healthCheck = {
      command     = ["CMD-SHELL", "wget -qO- http://localhost:${var.server_port}/readyz | grep -q '\"ok\":true'"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 120 # Aurora wake + boot-time migrations need headroom
    }
  }])
}

resource "aws_ecs_service" "server" {
  name            = "${var.project}-server"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.server.arn
  desired_count   = var.server_desired_count
  launch_type     = "FARGATE"
  network_configuration {
    subnets          = local.effective_subnets
    security_groups  = [aws_security_group.server.id]
    assign_public_ip = false
  }
  load_balancer {
    target_group_arn = aws_lb_target_group.server.arn
    container_name   = "server"
    container_port   = var.server_port
  }
  # Boot-time migrations run inside the container (deploy/server-bootstrap):
  # a fresh deploy replaces the task, which re-runs forward-only migrations
  # before joining the target group (readyz gates the health check above).
  depends_on = [aws_lb_listener.https]
}
