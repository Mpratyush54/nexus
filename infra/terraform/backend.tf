# backend.tf — remote-state example (issue #45).
#
# Working pattern: keep this partial `backend "s3" {}` block and supply the
# bucket/key/region at init time so no environment name is hardcoded:
#
#   terraform init \
#     -backend-config="bucket=central-memory-tfstate-<acct>" \
#     -backend-config="key=prod/terraform.tfstate" \
#     -backend-config="region=us-east-1" \
#     -backend-config="dynamodb_table=central-memory-tfstate-lock" \
#     -backend-config="encrypt=true"
#
# One-time bootstrap (before the first init above):
#   aws s3api create-bucket --bucket central-memory-tfstate-<acct> --region us-east-1
#   aws s3api put-bucket-versioning --bucket central-memory-tfstate-<acct> \
#     --versioning-configuration Status=Enabled
#   aws s3api put-bucket-encryption --bucket central-memory-tfstate-<acct> \
#     --server-side-encryption-configuration \
#     '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
#   aws dynamodb create-table --table-name central-memory-tfstate-lock \
#     --attribute-definitions AttributeName=LockID,AttributeType=S \
#     --key-schema AttributeName=LockID,KeyType=HASH \
#     --billing-mode PAY_PER_REQUEST --region us-east-1
#
# Local state remains the default when init runs WITHOUT -backend-config
# (dev iterations); CI prod applies MUST pass the backend config above.
terraform {
  backend "s3" {}
}
