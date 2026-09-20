# outputs.tf — connection surface for operators and CI (issue #20).

output "aurora_endpoint" {
  description = "Aurora Serverless v2 writer endpoint (psql / DATABASE_URL host)."
  value       = aws_rds_cluster.main.endpoint
}

output "db_name" {
  description = "Initial database name."
  value       = aws_rds_cluster.main.database_name
}

output "db_secret_arn" {
  description = "Secrets Manager ARN for app DB credentials (ECS consumes via secrets block)."
  value       = aws_secretsmanager_secret.db_app.arn
  sensitive   = true
}

output "jwt_secret_arn" {
  description = "Secrets Manager ARN for the JWT signing key."
  value       = aws_secretsmanager_secret.jwt.arn
  sensitive   = true
}

output "smtp_secret_arn" {
  description = "Secrets Manager ARN for SMTP (Brevo/SendGrid). Operator sets password."
  value       = aws_secretsmanager_secret.smtp.arn
  sensitive   = true
}

output "task_role_arn" {
  description = "ECS task role ARN (matches deploy/ecs-task.json taskRoleArn; least-privilege, no S3 until the cold-writer lands)."
  value       = aws_iam_role.task.arn
}

output "alb_dns_name" {
  description = "Public ALB DNS name fronting the server service."
  value       = aws_lb.server.dns_name
}

output "cold_events_bucket" {
  description = "S3 bucket for 180d+ cold event archive (Glacier transition)."
  value       = aws_s3_bucket.cold_events.id
}

output "snapshots_bucket" {
  description = "S3 bucket for exported memory snapshots / episode attachments."
  value       = aws_s3_bucket.snapshots.id
}

output "releases_bucket" {
  description = "S3 bucket for CLI/daemon/PWA versioned release artifacts (nexus update)."
  value       = aws_s3_bucket.releases.id
}

output "harvest_queue_url" {
  description = "SQS queue URL for harvest OpenRouter jobs (HARVEST_QUEUE_URL)."
  value       = aws_sqs_queue.harvest.url
}
