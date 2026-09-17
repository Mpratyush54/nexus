# aurora.tf — Aurora Serverless v2, PostgreSQL 16 + pgvector (issue #20).
#
# Locked Decisions: "AWS (RDS Aurora Serverless v2 + S3 + Secrets Manager) —
# scales to zero for early stage". Plan §1.1 / migrations/001_initial.up.sql
# require the pgcrypto + vector extensions; both ship in Aurora PG 16, and
# 001 creates them with CREATE EXTENSION at boot-time migrate.
#
# pgvector checklist item: the custom cluster parameter group below pins the
# aurora-postgresql16 family so the vector extension is available, and forces
# SSL (server container connects with DB_SSLMODE=require).

data "aws_vpc" "target" {
  count   = var.vpc_id == "" ? 1 : 0
  default = true
}

data "aws_subnets" "target" {
  count = var.vpc_id == "" ? 1 : 0
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.target[0].id]
  }
}

locals {
  effective_vpc_id  = var.vpc_id != "" ? var.vpc_id : data.aws_vpc.target[0].id
  effective_subnets = length(var.subnet_ids) > 0 ? var.subnet_ids : data.aws_subnets.target[0].ids
  # ALB placement (issue #45): the internet-facing load balancer must sit on
  # PUBLIC subnets. Explicit public_subnet_ids win; otherwise the app subnets
  # are reused (correct for the default VPC, whose subnets are public).
  effective_public_subnets = length(var.public_subnet_ids) > 0 ? var.public_subnet_ids : local.effective_subnets
}

# Custom parameter group: required so the cluster runs the PG16 family where
# the `vector` (pgvector) extension is installable. CREATE EXTENSION itself
# happens in migration 001 at container boot, not here.
resource "aws_rds_cluster_parameter_group" "pgvector" {
  name        = "${var.project}-pg16-pgvector"
  family      = "aurora-postgresql16"
  description = "Aurora PG16 family for central-memory: enables pgvector/pgcrypto via migration 001_initial (issue #20)."

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }
}

resource "aws_db_subnet_group" "main" {
  name       = "${var.project}-db-subnets"
  subnet_ids = local.effective_subnets
  tags       = { Name = "${var.project}-db-subnets" }
}

resource "aws_security_group" "db" {
  name        = "${var.project}-db"
  description = "Aurora ingress: Postgres from Fargate tasks only (issue #20)."
  vpc_id      = local.effective_vpc_id

  ingress {
    description     = "Postgres from server tasks"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.server.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_rds_cluster" "main" {
  cluster_identifier = "${var.project}-aurora"
  engine             = "aurora-postgresql"
  engine_version     = "16"
  database_name      = var.db_name
  master_username    = var.db_username
  # Password comes from Secrets Manager (managed_master_user_password lets RDS
  # own rotation); never a Terraform variable or literal.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.db.arn

  db_subnet_group_name            = aws_db_subnet_group.main.name
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.pgvector.name
  vpc_security_group_ids          = [aws_security_group.db.id]
  storage_encrypted               = true
  kms_key_id                      = aws_kms_key.db.arn
  deletion_protection             = true
  copy_tags_to_snapshot           = true
  backup_retention_period         = 7
  preferred_backup_window         = "03:00-04:00"

  serverlessv2_scaling_configuration {
    min_capacity = var.aurora_min_acu
    max_capacity = var.aurora_max_acu
  }
}

resource "aws_rds_cluster_instance" "writer" {
  identifier          = "${var.project}-aurora-1"
  cluster_identifier  = aws_rds_cluster.main.id
  instance_class      = "db.serverless"
  engine              = aws_rds_cluster.main.engine
  engine_version      = aws_rds_cluster.main.engine_version
  publicly_accessible = false
}

resource "aws_kms_key" "db" {
  description             = "Encrypts Aurora storage + master-user secret (issue #20)."
  deletion_window_in_days = 7
  enable_key_rotation     = true
}
