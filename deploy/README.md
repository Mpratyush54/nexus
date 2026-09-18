# deploy/ — AWS production deployment templates

For nexus issue #20 ([Phase 6] AWS Cloud Infrastructure & Production
Deployment). Templates and notes only — no `terraform apply`, no live
infrastructure changes.

## Contents

| File                | Purpose |
|---------------------|---------|
| `Dockerfile.server` | Production image for `central-server` (`./cmd/server`; multi-stage Go 1.26 → alpine 3.21 with `sh`+`psql`+`wget`, non-root, `*.up.sql` only) |
| `Dockerfile.daemon` | Image for `workspace-daemon` (`./cmd/daemon`; single port 7687, `WORKDIR /workspace`, `CENTRAL_SERVER_URL`/`CENTRAL_USER_ID` env) |
| `migrate.sh`        | Container ENTRYPOINT: resolves the DSN (`DATABASE_URL` or assembled from `DB_*`), enforces `sslmode=require` outside local-dev, applies `migrations/*.up.sql` via `psql`, then `exec`s the server. Forward-only: never touches `*.down.sql` |
| `ecs-task.json`     | SAMPLE Fargate task definition (fill in `ACCOUNT`/`REGION`/secret ARNs before registering; mirrors `infra/terraform`) |
| `rds-notes.md`      | Aurora Serverless v2 + pgvector enable/verify steps incl. master bootstrap + reboot note |
| `secrets-notes.md`  | Secrets Manager layout + ECS wiring + the canonical env contract |

## Build

Both entrypoints exist (`cmd/server/main.go`, `cmd/daemon/main.go` — the
"#74 missing binaries" gap is closed):

```bash
# From repo root:
docker build -f deploy/Dockerfile.server -t central-server:dev .
docker build -f deploy/Dockerfile.daemon -t workspace-daemon:dev .
# Compose smoke (build only; full up needs the daemon bind-flag fix, #126):
docker compose -f deploy/docker-compose.yml build
```

## Env contract (canonical — duplicated from `secrets-notes.md`)

- `DATABASE_URL` primary; discrete `DB_HOST`/`DB_PORT`/`DB_NAME`/`DB_USER`/`DB_PASSWORD` + `DB_SSLMODE` fallback assembled **identically** by `migrate.sh` and `cmd/server` (`resolveDatabaseURL`). Prod pins `sslmode=require` (Aurora `rds.force_ssl=1`); local compose uses `sslmode=disable` + `CENTRAL_MEMORY_LOCAL_DEV=1`.
- `JWT_SECRET` canonical signing key (`CENTRAL_MEMORY_JWT_KEY` still accepted as legacy fallback — `internal/server/auth.go` reads it).
- `MIGRATIONS_DIR` default `/migrations` (image path, compose, ECS, `/readyz` all agree).
- Ports: server `8080`, daemon `7687` (single port everywhere; `DAEMON_ADDR`/`DAEMON_PORT` are dead and unset).

## Boot order (server)

1. ECS injects `DB_*` (+ `DB_SSLMODE=require`) and `JWT_SECRET` from Secrets Manager.
2. `migrate.sh` resolves the DSN, refuses non-`require` sslmode outside local-dev, applies `migrations/*.up.sql` in order (001 enables `pgcrypto` + `vector` — needs the master bootstrap in `rds-notes.md` first).
3. Server starts: `/healthz` = liveness (ALB target), `/readyz` = readiness (ECS container check; DSN + sslmode gate).

## Decisions

Rationale (why Aurora Serverless v2, S3 cold archive, Secrets Manager,
ECS/Fargate, Docker): `docs/decisions/2026-09-17-aws-deploy.md`.
Env-contract decisions: `docs/decisions/ADR-045-deploy-env-contract.md`.
