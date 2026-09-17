# deploy/ — AWS production deployment templates

For nexus issue #20 ([Phase 6] AWS Cloud Infrastructure & Production
Deployment). Templates and notes only — no `terraform apply`, no live
infrastructure changes, no Go code changes.

## Contents

| File                | Purpose |
|---------------------|---------|
| `Dockerfile.server` | Production image for `central-server` (`./cmd/server`; multi-stage Go 1.26 → distroless, runs `migrate.sh` on boot) |
| `Dockerfile.daemon` | Image for `workspace-daemon` (`./cmd/daemon`; local-first binary, containerized for dev/test parity) |
| `migrate.sh`        | Container ENTRYPOINT: applies `migrations/*.up.sql` via `psql` (`DATABASE_URL`), then `exec`s the server |
| `ecs-task.json`     | SAMPLE Fargate task definition (fill in `ACCOUNT`/`REGION`/secret ARNs before registering) |
| `rds-notes.md`      | Aurora Serverless v2 + pgvector enable/verify steps |
| `secrets-notes.md`  | Secrets Manager layout + ECS wiring |

## Build (once `cmd/server` / `cmd/daemon` land per the plan's target structure)

```bash
# From repo root:
docker build -f deploy/Dockerfile.server -t central-server:dev .
docker build -f deploy/Dockerfile.daemon -t workspace-daemon:dev .
```

> `cmd/server` and `cmd/daemon` do not exist yet (only `cmd/nexus` does), so
> both Dockerfiles intentionally fail fast until those entrypoints land.

## Boot order (server)

1. ECS injects `DATABASE_URL` + `CENTRAL_MEMORY_JWT_KEY` from Secrets Manager.
2. `migrate.sh` applies `migrations/*.up.sql` in order (001 enables
   `pgcrypto` + `vector`).
3. Server starts, serves `/healthz` (used by the `ecs-task.json` health check).

## Decisions

Rationale (why Aurora Serverless v2, S3 cold archive, Secrets Manager,
ECS/Fargate, Docker): `docs/decisions/2026-09-17-aws-deploy.md`.
