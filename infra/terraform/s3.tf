# s3.tf — cold event archive + snapshot exports (issue #20, plan §6.2).
#
# Retention policy from the plan:
#   0–30 days   hot   (primary events table)
#   30–180 days warm  (partitioned archive)
#   180+ days   cold  (payload dropped, raw events -> S3 Glacier)
# The archive bucket below encodes the 180-day cold transition; the
# snapshots bucket holds exported memory snapshots / episode attachments.
#
# HONEST SCOPE NOTE (#128): the buckets are hardened infra with NO code path
# yet — nothing in go.mod (no AWS SDK) reads/writes S3, ecs.tf grants the
# task role no S3 access (least privilege), and ADR-020 admits the
# cold-writer is a follow-up. The S3 Gateway VPC endpoint below keeps
# private-subnet Fargate -> S3 off NAT (cost) for when the writer lands.
# Storage class is GLACIER_IR (Instant Retrieval): no rehydration path to
# build, unlike GLACIER/Flexible Retrieval.

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
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
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
# filter {} (whole bucket) is REQUIRED by provider v5+ (#128) — without it
# `terraform apply` errors.
resource "aws_s3_bucket_lifecycle_configuration" "cold_events" {
  bucket = aws_s3_bucket.cold_events.id
  rule {
    id     = "cold-to-glacier-ir"
    status = "Enabled"
    filter {}
    transition {
      days          = 180
      storage_class = "GLACIER_IR"
    }
    noncurrent_version_expiration { noncurrent_days = 90 }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "snapshots" {
  bucket = aws_s3_bucket.snapshots.id
  rule {
    id     = "snapshot-version-expiry"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration { noncurrent_days = 90 }
  }
}

resource "aws_s3_bucket" "releases" {
  bucket = "${var.project}-releases"
}

resource "aws_s3_bucket_versioning" "releases" {
  bucket = aws_s3_bucket.releases.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "releases" {
  bucket = aws_s3_bucket.releases.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_public_access_block" "releases" {
  bucket                  = aws_s3_bucket.releases.id
  block_public_acls       = false
  block_public_policy     = false
  ignore_public_acls      = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_policy" "releases_public_read" {
  bucket = aws_s3_bucket.releases.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "PublicReadCLI"
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.releases.arn}/*"
    }]
  })
  depends_on = [aws_s3_bucket_public_access_block.releases]
}

# Gateway endpoint so private-subnet tasks reach S3 without NAT (cost) or
# blackholing where no NAT exists (#128). Uses the caller's route tables
# (var.private_route_table_ids); empty = endpoint created without
# associations — set the var to attach.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = local.effective_vpc_id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = var.private_route_table_ids
  tags              = { Name = "${var.project}-s3-gateway" }
}
