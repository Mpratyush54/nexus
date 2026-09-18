# variables.tf — tunable inputs; no secrets have defaults (they are generated
# into Secrets Manager by secrets.tf, never hardcoded).

variable "region" {
  description = "AWS region for all issue #20 resources."
  type        = string
  default     = "us-east-1"
}

variable "project" {
  description = "Name prefix for every resource (buckets, cluster, services)."
  type        = string
  default     = "central-memory"
}

variable "vpc_id" {
  description = "VPC hosting Aurora + Fargate. Empty means 'use the default VPC' (resolved in aurora.tf/ecs.tf via data source)."
  type        = string
  default     = ""
}

variable "subnet_ids" {
  description = "Legacy: subnets for everything. Prefer public_subnet_ids + private_subnet_ids (issue #45); when those are empty this backfills both."
  type        = list(string)
  default     = []
}

variable "public_subnet_ids" {
  description = "Public subnets for the ALB (issue #45). Empty falls back to subnet_ids / default-VPC subnets."
  type        = list(string)
  default     = []
}

variable "private_subnet_ids" {
  description = "Private subnets for Fargate tasks + Aurora (issue #45). Empty falls back to subnet_ids / default-VPC subnets."
  type        = list(string)
  default     = []
}

variable "private_route_table_ids" {
  description = "Private route tables to associate with the S3 Gateway endpoint (issue #128). Empty creates the endpoint unattached."
  type        = list(string)
  default     = []
}

variable "aurora_engine_version" {
  description = "Pinned Aurora PostgreSQL version (issue #127). Bare major ('16') is rejected — use a full version. Upgrade path in deploy/rds-notes.md."
  type        = string
  default     = "16.6"
}

variable "aurora_min_acu" {
  description = "Aurora Serverless v2 minimum capacity. 0.5 is the engine minimum — early-stage scale-to-near-zero per Locked Decisions."
  type        = number
  default     = 0.5
}

variable "aurora_max_acu" {
  description = "Aurora Serverless v2 maximum capacity."
  type        = number
  default     = 4
}

variable "db_name" {
  description = "Initial database name."
  type        = string
  default     = "central_memory"
}

variable "db_username" {
  description = "Master username (password is RDS-managed into Secrets Manager; the app never uses this — see app_username)."
  type        = string
  default     = "central"
}

variable "app_username" {
  description = "Dedicated app DB user (issue #127). Bootstrapped once from the master (CREATE USER + GRANT, see deploy/rds-notes.md); its password is the db-app secret."
  type        = string
  default     = "central_app"
}

variable "skip_final_snapshot" {
  description = "Skip the final Aurora snapshot on destroy (issue #127). false (default) is safe; true for ephemeral stacks."
  type        = bool
  default     = false
}

variable "server_cpu" {
  description = "Fargate CPU units for the server task (512 = 0.5 vCPU; sane for a REST+WS API)."
  type        = number
  default     = 512
}

variable "server_memory" {
  description = "Fargate memory (MiB) for the server task. Must be a valid 512-CPU pairing (1024/2048/3072/4096)."
  type        = number
  default     = 1024
}

variable "server_desired_count" {
  description = "Running server tasks. 1 is correct for v1 (single writer runs migrations on boot; scale after the Postgres adapter lands)."
  type        = number
  default     = 1
}

variable "server_port" {
  description = "Container port the server listens on ($PORT)."
  type        = number
  default     = 8080
}

variable "server_image" {
  description = "ECR image URI for the server (built from Dockerfile.server; pushed by CI). Empty by default: terraform plan with the placeholder is rejected so nobody deploys central-memory-server:latest by accident (issue #126). CI injects ACCOUNT.dkr.ecr.REGION:SHA."
  type        = string
  default     = ""

  validation {
    condition     = var.server_image != "" && can(regex("^.+\\.dkr\\.ecr\\..+\\.amazonaws\\.com/", var.server_image))
    error_message = "server_image must be a full ECR URI (ACCOUNT.dkr.ecr.REGION.amazonaws.com/repo:tag); CI injects it. The old central-memory-server:latest default is not pullable."
  }
}

variable "api_domain" {
  description = "Public domain for the ALB HTTPS listener (must have an ISSUED ACM cert at apply time)."
  type        = string
  default     = "api.example.com"
}
