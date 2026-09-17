# Secrets notes — Secrets Manager wiring (template)

> Templates/notes only. No live secret creation.

## Secrets (two, minimal)

| Secret name                  | Key(s)         | Consumed as              | Used by        |
|------------------------------|----------------|--------------------------|----------------|
| `central-memory/database-url`| `database_url` | `DATABASE_URL` env       | server, migrate.sh |
| `central-memory/jwt-key`     | `jwt_key`      | `CENTRAL_MEMORY_JWT_KEY` | server (`auth.go` stub → future `jwt/v5`) |

## Create (manual, one-time — example)

```bash
aws secretsmanager create-secret \
  --name central-memory/database-url \
  --secret-string '{"database_url":"postgres://USER:PASS@CLUSTER-ENDPOINT:5432/central_memory?sslmode=require"}'

aws secretsmanager create-secret \
  --name central-memory/jwt-key \
  --secret-string '{"jwt_key":"<32+ random bytes, base64>"}'
```

Generate the JWT key locally, never commit it:

```bash
# PowerShell:
# [Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Max 256 }))
```

## ECS wiring

- The task execution role needs `secretsmanager:GetSecretValue` on both ARNs.
- `deploy/ecs-task.json` injects them via `containerDefinitions[].secrets`
  (`valueFrom` = `secret-arn:key:version-stage:version-id`); containers see
  plain env vars, secrets never land in the image or task JSON.
- Rotation: rotate `database-url` in Secrets Manager, then force a new
  deployment (`aws ecs update-service --force-new-deployment`). Stagger: Aurora
  credential rotation briefly invalidates old passwords — prefer a maintenance
  window until RDS-managed rotation is configured.

## Local dev

- `DATABASE_URL` + `CENTRAL_MEMORY_JWT_KEY` as plain env vars (dev fallback in
  `internal/server/auth.go`); production must always come from Secrets Manager.
