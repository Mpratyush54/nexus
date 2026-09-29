#!/usr/bin/env bash
set -euo pipefail

# CI role can write the account-id mirror bucket. Cloudflare serves the domain
# bucket (nexus.pratyushes.dev). Sync the mirror always; best-effort mirror→live.
# CloudFront is optional — production portal is Cloudflare → S3 live bucket.
AWS_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-south-1}}"
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION"

BUCKET="${FRONTEND_S3_BUCKET:-central-memory-frontend-833291393451}"
LIVE_BUCKET="${FRONTEND_LIVE_S3_BUCKET:-nexus.pratyushes.dev}"
echo "==> Target S3 Bucket: $BUCKET (live mirror: $LIVE_BUCKET) region=$AWS_REGION"

# Portable temp. Windows-native AWS CLI cannot read Git Bash /tmp mounts —
# prefer a real Windows path when cygpath is available.
TMP_DIR="${TMPDIR:-${TMP:-${TEMP:-/tmp}}}"
if command -v cygpath >/dev/null 2>&1; then
  if [ -n "${LOCALAPPDATA:-}" ]; then
    TMP_DIR="$(cygpath -u "$LOCALAPPDATA")/Temp"
  elif [ -n "${TEMP:-}" ]; then
    TMP_DIR="$(cygpath -u "$TEMP")"
  fi
  mkdir -p "$TMP_DIR"
fi
TMP_DIR="${TMP_DIR%/}"
POLICY_FILE="${TMP_DIR}/nexus-frontend-bucket-policy-$$.json"
CF_CONFIG_FILE="${TMP_DIR}/nexus-frontend-cf-config-$$.json"
cleanup() { rm -f "$POLICY_FILE" "$CF_CONFIG_FILE" 2>/dev/null || true; }
trap cleanup EXIT

aws_file_uri() {
  local f="$1"
  if command -v cygpath >/dev/null 2>&1; then
    # file://C:/Users/... (forward slashes) works with AWS CLI on Windows.
    printf 'file://%s' "$(cygpath -m "$f")"
  else
    printf 'file://%s' "$f"
  fi
}

sync_bucket() {
  local target="$1"
  echo "==> Syncing frontend assets to s3://${target}..."
  aws s3 sync frontend/dist "s3://${target}" --delete \
    --cache-control "public,max-age=31536000,immutable" \
    --exclude "index.html" --exclude "sw.js" --exclude "manifest.webmanifest"

  aws s3 cp frontend/dist/index.html "s3://${target}/index.html" \
    --cache-control "public,max-age=60" --content-type "text/html"

  aws s3 cp frontend/dist/sw.js "s3://${target}/sw.js" \
    --cache-control "public,max-age=60" --content-type "application/javascript"

  aws s3 cp frontend/dist/manifest.webmanifest "s3://${target}/manifest.webmanifest" \
    --cache-control "public,max-age=60" --content-type "application/manifest+json"

  aws s3 website "s3://${target}" --index-document index.html --error-document index.html 2>/dev/null || true
}

if ! aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
  echo "Creating S3 bucket $BUCKET in ${AWS_REGION}..."
  aws s3api create-bucket --bucket "$BUCKET" --region "${AWS_REGION}" \
    --create-bucket-configuration LocationConstraint="${AWS_REGION}" 2>/dev/null || true
fi

aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration "BlockPublicAcls=false,IgnorePublicAcls=false,BlockPublicPolicy=false,RestrictPublicBuckets=false" 2>/dev/null || true

cat <<POLICY > "$POLICY_FILE"
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PublicReadGetObject",
      "Effect": "Allow",
      "Principal": "*",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::${BUCKET}/*"
    }
  ]
}
POLICY
aws s3api put-bucket-policy --bucket "$BUCKET" --policy "$(aws_file_uri "$POLICY_FILE")" || true

sync_bucket "$BUCKET"

if [ "$LIVE_BUCKET" != "$BUCKET" ]; then
  set +e
  aws s3api head-bucket --bucket "$LIVE_BUCKET" >/dev/null 2>&1
  live_ok=$?
  if [ "$live_ok" -eq 0 ]; then
    sync_bucket "$LIVE_BUCKET"
    live_ok=$?
  fi
  set -e
  if [ "$live_ok" -eq 0 ]; then
    echo "==> Live bucket ${LIVE_BUCKET} updated"
  else
    echo "==> WARNING: could not sync live bucket ${LIVE_BUCKET} (grant CI role s3 access); mirror ${BUCKET} is current"
  fi
fi

echo "==> Resolving CloudFront distribution..."
DIST_ID="${FRONTEND_CLOUDFRONT_ID:-}"
if [ -z "$DIST_ID" ]; then
  DIST_ID=$(aws cloudfront list-distributions --query "DistributionList.Items[?contains(Origins.Items[0].DomainName, '$BUCKET')].Id" --output text 2>/dev/null || true)
  # Normalize "None" / whitespace-only from AWS CLI.
  if [ "$DIST_ID" = "None" ] || [ -z "${DIST_ID//[[:space:]]/}" ]; then
    DIST_ID=""
  fi
fi

CF_DOMAIN=""
CREATE_CF="${FRONTEND_CREATE_CLOUDFRONT:-0}"
if [ -z "$DIST_ID" ]; then
  if [ "$CREATE_CF" = "1" ]; then
    echo "Creating CloudFront distribution for $BUCKET with SPA fallback routing..."
    cat <<CFCONFIG > "$CF_CONFIG_FILE"
{
  "CallerReference": "nexus-frontend-${BUCKET}",
  "Comment": "Nexus React PWA Frontend",
  "Enabled": true,
  "DefaultRootObject": "index.html",
  "Origins": {
    "Quantity": 1,
    "Items": [
      {
        "Id": "S3-nexus-frontend",
        "DomainName": "${BUCKET}.s3-website.${AWS_REGION}.amazonaws.com",
        "CustomOriginConfig": {
          "HTTPPort": 80,
          "HTTPSPort": 443,
          "OriginProtocolPolicy": "http-only"
        }
      }
    ]
  },
  "DefaultCacheBehavior": {
    "TargetOriginId": "S3-nexus-frontend",
    "ViewerProtocolPolicy": "redirect-to-https",
    "AllowedMethods": {
      "Quantity": 2,
      "Items": ["GET", "HEAD"]
    },
    "ForwardedValues": {
      "QueryString": false,
      "Cookies": { "Forward": "none" }
    },
    "MinTTL": 0,
    "DefaultTTL": 86400,
    "MaxTTL": 31536000
  },
  "CustomErrorResponses": {
    "Quantity": 1,
    "Items": [
      {
        "ErrorCode": 404,
        "ResponsePagePath": "/index.html",
        "ResponseCode": "200",
        "ErrorCachingMinTTL": 0
      }
    ]
  }
}
CFCONFIG
    DIST_RES=$(aws cloudfront create-distribution --distribution-config "$(aws_file_uri "$CF_CONFIG_FILE")" 2>/dev/null || true)
    DIST_ID=$(echo "$DIST_RES" | grep -o '"Id": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
    CF_DOMAIN=$(echo "$DIST_RES" | grep -o '"DomainName": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
  else
    echo "==> No CloudFront distribution found (ok: portal is Cloudflare → s3://${LIVE_BUCKET})"
  fi
fi

if [ -n "$DIST_ID" ]; then
  CF_DOMAIN=$(aws cloudfront get-distribution --id "$DIST_ID" --query "Distribution.DomainName" --output text 2>/dev/null || true)
  echo "Found active CloudFront distribution: $DIST_ID ($CF_DOMAIN)"
  echo "Creating cache invalidation..."
  if aws cloudfront create-invalidation --distribution-id "$DIST_ID" --paths "/*"; then
    echo "==> CloudFront invalidation submitted"
  else
    echo "==> WARNING: CloudFront invalidation failed (S3 sync still succeeded)"
  fi
fi

echo "=========================================================="
echo "S3 Bucket: $BUCKET"
echo "Live Bucket: $LIVE_BUCKET"
echo "S3 Website Endpoint: http://${BUCKET}.s3-website.${AWS_REGION}.amazonaws.com"
if [ -n "${CF_DOMAIN:-}" ]; then
  echo "CloudFront Distribution: $DIST_ID"
  echo "CloudFront Domain: https://$CF_DOMAIN"
  echo "Cloudflare CNAME Target: $CF_DOMAIN"
fi
echo "Portal: https://nexus.pratyushes.dev"
echo "=========================================================="
