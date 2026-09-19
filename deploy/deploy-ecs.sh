#!/usr/bin/env bash
set -euo pipefail

echo "==> Ensuring task execution role can read central-memory/github + openrouter secrets..."
aws iam put-role-policy --role-name central-memory-task-exec \
  --policy-name central-memory-read-github-secret \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:'"${AWS_REGION}"':833291393451:secret:central-memory/github*"}]}' 2>/dev/null || true
aws iam put-role-policy --role-name central-memory-task-exec \
  --policy-name central-memory-read-openrouter-secret \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:'"${AWS_REGION}"':833291393451:secret:openrouter*"}]}' 2>/dev/null || true

echo "==> Finding active ECS cluster and service..."
CLUSTER="central-memory-cluster"
SERVICE="central-memory-srv"

if ! aws ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query "services[0].status" --output text 2>/dev/null | grep -q "ACTIVE"; then
  if aws ecs describe-services --cluster "$CLUSTER" --services "central-memory-server" --query "services[0].status" --output text 2>/dev/null | grep -q "ACTIVE"; then
    SERVICE="central-memory-server"
  elif aws ecs describe-services --cluster "central-memory" --services "central-memory-srv" --query "services[0].status" --output text 2>/dev/null | grep -q "ACTIVE"; then
    CLUSTER="central-memory"
    SERVICE="central-memory-srv"
  elif aws ecs describe-services --cluster "central-memory" --services "central-memory-server" --query "services[0].status" --output text 2>/dev/null | grep -q "ACTIVE"; then
    CLUSTER="central-memory"
    SERVICE="central-memory-server"
  fi
fi
echo "Active target: cluster=$CLUSTER, service=$SERVICE"

CURRENT_TASK_DEF=$(aws ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" --query "services[0].taskDefinition" --output text)
echo "Current running task definition: $CURRENT_TASK_DEF"

IMAGE="${REGISTRY}/${ECR_REPOSITORY}:${IMAGE_TAG}"
echo "Target Image: $IMAGE"

echo "==> Preparing updated task definition JSON..."
aws ecs describe-task-definition --task-definition "$CURRENT_TASK_DEF" | \
jq --arg img "$IMAGE" \
   --arg api "https://api-nexus.pratyushes.dev" \
   --arg app "https://nexus.pratyushes.dev" \
   --arg redir "https://api-nexus.pratyushes.dev/auth/github/callback" \
   --arg cid "Ov23li997FyAUgaucZQO" \
   --arg sec "arn:aws:secretsmanager:${AWS_REGION}:833291393451:secret:central-memory/github:client_secret::" \
   --arg orkey "arn:aws:secretsmanager:${AWS_REGION}:833291393451:secret:openrouter:api_key::" \
   '.taskDefinition | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities, .registeredAt, .registeredBy) |
    .containerDefinitions[0].image = $img |
    .containerDefinitions[0].environment = [
      (.containerDefinitions[0].environment // [] | .[] | select(.name != "PUBLIC_API_URL" and .name != "PUBLIC_APP_URL" and .name != "GITHUB_OAUTH_REDIRECT" and .name != "GITHUB_CLIENT_ID" and .name != "OPENROUTER_MODEL")),
      {name: "PUBLIC_API_URL", value: $api},
      {name: "PUBLIC_APP_URL", value: $app},
      {name: "GITHUB_OAUTH_REDIRECT", value: $redir},
      {name: "GITHUB_CLIENT_ID", value: $cid},
      {name: "OPENROUTER_MODEL", value: "openrouter/free"}
    ] |
    .containerDefinitions[0].secrets = [
      (.containerDefinitions[0].secrets // [] | .[] | select(.name != "GITHUB_CLIENT_SECRET" and .name != "OPENROUTER_API_KEY")),
      {name: "GITHUB_CLIENT_SECRET", valueFrom: $sec},
      {name: "OPENROUTER_API_KEY", valueFrom: $orkey}
    ]' > /tmp/task-def.json

echo "==> Registering new ECS task definition revision..."
REGISTERED_ARN=$(aws ecs register-task-definition --cli-input-json file:///tmp/task-def.json --query 'taskDefinition.taskDefinitionArn' --output text)
echo "Registered new task definition: $REGISTERED_ARN"

echo "==> Updating ECS service with new task definition and forcing deployment..."
aws ecs update-service --cluster "$CLUSTER" --service "$SERVICE" --task-definition "$REGISTERED_ARN" --force-new-deployment

echo "==> ECS deployment successfully initiated!"
