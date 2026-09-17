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
