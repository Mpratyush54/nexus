#!/usr/bin/env bash
set -euo pipefail

echo "==> Ensuring task execution role can read central-memory/github secret..."
aws iam put-role-policy --role-name central-memory-task-exec \
  --policy-name central-memory-read-github-secret \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:'"${AWS_REGION}"':833291393451:secret:central-memory/github*"}]}' 2>/dev/null || true

echo "==> Resolving active ECS task definition..."
TASK_DEF_FAMILY="central-memory-server"
if ! aws ecs describe-task-definition --task-definition "$TASK_DEF_FAMILY" &>/dev/null; then
  TASK_DEF_FAMILY="central-memory-srv"
fi
echo "Using task definition family: $TASK_DEF_FAMILY"

IMAGE="${REGISTRY}/${ECR_REPOSITORY}:${IMAGE_TAG}"
echo "Target Image: $IMAGE"

echo "==> Preparing updated task definition JSON..."
aws ecs describe-task-definition --task-definition "$TASK_DEF_FAMILY" | \
jq --arg img "$IMAGE" \
   --arg api "https://api-nexus.pratyushes.dev" \
   --arg app "https://nexus.pratyushes.dev" \
   --arg redir "https://api-nexus.pratyushes.dev/auth/github/callback" \
   --arg cid "Ov23li997FyAUgaucZQO" \
   --arg sec "arn:aws:secretsmanager:${AWS_REGION}:833291393451:secret:central-memory/github:client_secret::" \
   '.taskDefinition | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities, .registeredAt, .registeredBy) |
    .containerDefinitions[0].image = $img |
    .containerDefinitions[0].environment = [
      (.containerDefinitions[0].environment // [] | .[] | select(.name != "PUBLIC_API_URL" and .name != "PUBLIC_APP_URL" and .name != "GITHUB_OAUTH_REDIRECT" and .name != "GITHUB_CLIENT_ID")),
      {name: "PUBLIC_API_URL", value: $api},
      {name: "PUBLIC_APP_URL", value: $app},
      {name: "GITHUB_OAUTH_REDIRECT", value: $redir},
      {name: "GITHUB_CLIENT_ID", value: $cid}
    ] |
    .containerDefinitions[0].secrets = [
      (.containerDefinitions[0].secrets // [] | .[] | select(.name != "GITHUB_CLIENT_SECRET")),
      {name: "GITHUB_CLIENT_SECRET", valueFrom: $sec}
    ]' > /tmp/task-def.json

echo "==> Registering new ECS task definition revision..."
REGISTERED_ARN=$(aws ecs register-task-definition --cli-input-json file:///tmp/task-def.json --query 'taskDefinition.taskDefinitionArn' --output text)
echo "Registered: $REGISTERED_ARN"

echo "==> Updating ECS service with new task definition and force deployment..."
aws ecs update-service --cluster central-memory-cluster --service central-memory-srv --task-definition "$REGISTERED_ARN" --force-new-deployment || \
aws ecs update-service --cluster central-memory-cluster --service central-memory-server --task-definition "$REGISTERED_ARN" --force-new-deployment || \
aws ecs update-service --cluster central-memory --service central-memory-srv --task-definition "$REGISTERED_ARN" --force-new-deployment || \
aws ecs update-service --cluster central-memory --service central-memory-server --task-definition "$REGISTERED_ARN" --force-new-deployment

echo "==> ECS deployment initiated successfully!"
