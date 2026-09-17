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
  # Remote state is intentionally NOT configured here: the team picks the
  # backend (S3 + DynamoDB lock) at apply time via -backend-config.
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
