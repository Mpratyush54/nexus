# s3.tf — cold event archive + snapshot exports (issue #20, plan §6.2).
#
# Retention policy from the plan:
#   0–30 days   hot   (primary events table)
#   30–180 days warm  (partitioned archive)
#   180+ days   cold  (payload dropped, raw events -> S3 Glacier)
# The archive bucket below encodes the 180-day Glacier transition; the
# snapshots bucket holds exported memory snapshots / episode attachments.

resource "aws_s3_bucket" "cold_events" {
  bucket = "${var.project}-cold-events"
}

resource "aws_s3_bucket" "snapshots" {
  bucket = "${var.project}-snapshots"
}

resource "aws_s3_bucket_versioning" "cold_events" {
  bucket = aws_s3_bucket.cold_events.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_versioning" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "cold_events" {
  bucket = aws_s3_bucket.cold_events.id
  rule { apply_server_side_encryption_by_default { sse_algorithm = "AES256" } }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule { apply_server_side_encryption_by_default { sse_algorithm = "AES256" } }
}

resource "aws_s3_bucket_public_access_block" "cold_events" {
  bucket                  = aws_s3_bucket.cold_events.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_public_access_block" "snapshots" {
  bucket                  = aws_s3_bucket.snapshots.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# 180d+ cold rule per plan §6.2; noncurrent versions expire to bound cost.
resource "aws_s3_bucket_lifecycle_configuration" "cold_events" {
  bucket = aws_s3_bucket.cold_events.id
  rule {
    id     = "cold-to-glacier"
    status = "Enabled"
    transition {
      days          = 180
      storage_class = "GLACIER"
    }
    noncurrent_version_expiration { noncurrent_days = 90 }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule {
    id     = "snapshot-version-expiry"
    status = "Enabled"
    noncurrent_version_expiration { noncurrent_days = 90 }
  }
}
