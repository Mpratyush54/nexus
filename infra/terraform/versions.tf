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
  # Remote state (#45): S3 + DynamoDB lock. Intentionally left as a
  # DOCUMENTED example rather than an active backend block so `terraform
  # validate` stays credential-free in CI. To enable: create the bucket +
  # table once, uncomment, and `terraform init -reconfigure`.
  #
  #   backend "s3" {
  #     bucket         = "central-memory-tfstate-<ACCOUNT>"
  #     key            = "central-memory/prod.tfstate"
  #     region         = "us-east-1"
  #     encrypt        = true
  #     dynamodb_table = "central-memory-tf-locks"
  #   }
  #
  # Bootstrap (one-time, manual):
  #   aws s3api create-bucket --bucket central-memory-tfstate-<ACCOUNT>
  #   aws dynamodb create-table --table-name central-memory-tf-locks \
  #     --attribute-definitions AttributeName=LockID,AttributeType=S \
  #     --key-schema AttributeName=LockID,KeyType=HASH \
  #     --billing-mode PAY_PER_REQUEST
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
