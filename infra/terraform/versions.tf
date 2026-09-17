# versions.tf — provider pins for issue #20 (plan: AWS Aurora + S3 + Secrets Manager).
terraform {
  required_version = ">= 1.9.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
  # Remote state: partial S3 backend in backend.tf (issue #45) — supply
  # bucket/key/region/lock table at init time via -backend-config (see the
  # example there). Plain `terraform init` keeps local state for dev.
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Project   = "central-memory"
      ManagedBy = "terraform"
      Issue     = "20"
    }
  }
}
