#!/usr/bin/env bash
set -euo pipefail

BUCKET="${FRONTEND_S3_BUCKET:-central-memory-frontend-833291393451}"
echo "==> Target S3 Bucket: $BUCKET"

if ! aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
  echo "Creating S3 bucket $BUCKET in ${AWS_REGION}..."
  aws s3api create-bucket --bucket "$BUCKET" --region "${AWS_REGION}" \
    --create-bucket-configuration LocationConstraint="${AWS_REGION}" 2>/dev/null || true
fi

aws s3api put-public-access-block --bucket "$BUCKET" \
  --public-access-block-configuration "BlockPublicAcls=false,IgnorePublicAcls=false,BlockPublicPolicy=false,RestrictPublicBuckets=false" 2>/dev/null || true

aws s3 website "s3://${BUCKET}" --index-document index.html --error-document index.html 2>/dev/null || true

cat <<POLICY > /tmp/bucket-policy.json
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
aws s3api put-bucket-policy --bucket "$BUCKET" --policy file:///tmp/bucket-policy.json || true

echo "==> Syncing frontend assets to S3..."
aws s3 sync frontend/dist "s3://${BUCKET}" --delete \
  --cache-control "public,max-age=31536000,immutable" \
  --exclude "index.html" --exclude "sw.js" --exclude "manifest.webmanifest"

aws s3 cp frontend/dist/index.html "s3://${BUCKET}/index.html" \
  --cache-control "public,max-age=60" --content-type "text/html"

aws s3 cp frontend/dist/sw.js "s3://${BUCKET}/sw.js" \
  --cache-control "public,max-age=60" --content-type "application/javascript"

aws s3 cp frontend/dist/manifest.webmanifest "s3://${BUCKET}/manifest.webmanifest" \
  --cache-control "public,max-age=60" --content-type "application/manifest+json"

echo "==> Resolving CloudFront distribution..."
DIST_ID="${FRONTEND_CLOUDFRONT_ID:-}"
if [ -z "$DIST_ID" ]; then
  DIST_ID=$(aws cloudfront list-distributions --query "DistributionList.Items[?contains(Origins.Items[0].DomainName, '$BUCKET')].Id" --output text 2>/dev/null || true)
fi

CF_DOMAIN=""
if [ -z "$DIST_ID" ] || [ "$DIST_ID" = "None" ]; then
  echo "Creating CloudFront distribution for $BUCKET with SPA fallback routing..."
  cat <<CFCONFIG > /tmp/cf-config.json
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
  DIST_RES=$(aws cloudfront create-distribution --distribution-config file:///tmp/cf-config.json 2>/dev/null || true)
  DIST_ID=$(echo "$DIST_RES" | grep -o '"Id": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
  CF_DOMAIN=$(echo "$DIST_RES" | grep -o '"DomainName": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
else
  CF_DOMAIN=$(aws cloudfront get-distribution --id "$DIST_ID" --query "Distribution.DomainName" --output text 2>/dev/null || true)
  echo "Found active CloudFront distribution: $DIST_ID ($CF_DOMAIN)"
  echo "Creating cache invalidation..."
  aws cloudfront create-invalidation --distribution-id "$DIST_ID" --paths "/*" || true
fi

echo "=========================================================="
echo "S3 Bucket: $BUCKET"
echo "S3 Website Endpoint: http://${BUCKET}.s3-website.${AWS_REGION}.amazonaws.com"
if [ -n "$CF_DOMAIN" ]; then
  echo "CloudFront Distribution: $DIST_ID"
  echo "CloudFront Domain: https://$CF_DOMAIN"
  echo "Cloudflare CNAME Target: $CF_DOMAIN"
fi
echo "=========================================================="
