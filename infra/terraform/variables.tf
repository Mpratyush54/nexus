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
  description = "Private subnets for Aurora + Fargate. Empty means 'use the default VPC subnets'. Set explicitly when vpc_id is custom."
  type        = list(string)
  default     = []
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
  description = "Master username (password is generated into Secrets Manager)."
  type        = string
  default     = "central"
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
  description = "ECR image URI for the server (built from Dockerfile.server; pushed by CI)."
  type        = string
  default     = "central-memory-server:latest"
}

variable "api_domain" {
  description = "Public domain for the ALB HTTPS listener (must have an ISSUED ACM cert at apply time)."
  type        = string
  default     = "api.example.com"
}
