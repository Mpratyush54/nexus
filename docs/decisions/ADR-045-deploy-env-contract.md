# ADR-045 — Deploy Env Contract: DATABASE_URL-with-DB_*-fallback + JWT_SECRET

- **ADR ID:** ADR-045-deploy-env-contract
- **Date:** 2026-09-18
- **Issues:** #45, #74, #112, #126, #127, #128
- **Status:** Accepted

## Context

Six deploy issues disagreed on environment wiring: `ecs.tf` injected discrete
`DB_*` parts, `migrate.sh` demanded `DATABASE_URL`, compose used `DB_*` with
`DB_SSLMODE=disable`, `ecs-task.json` referenced `DATABASE_URL` +
`CENTRAL_MEMORY_JWT_KEY`, `secrets-notes.md` documented different secret names
than `secrets.tf` created, and `MIGRATIONS_DIR` was `/migrations` in the image
but `/app/migrations` in ECS. Nothing agreed, so every consumer guessed.

## Decision (single source of truth; `deploy/secrets-notes.md` mirrors this)

1. **Database: `DATABASE_URL` primary, `DB_*` fallback.** When `DATABASE_URL`
   is set it is used verbatim. Otherwise it is assembled from `DB_HOST`,
   `DB_PORT` (default `5432`), `DB_NAME`, `DB_USER`, `DB_PASSWORD`, plus
   `DB_SSLMODE` (default `require`; `disable` only for local dev). The two
   builders — `deploy/migrate.sh` (shell) and `cmd/server/main.go`
   `resolveDatabaseURL` (Go) — implement identical precedence; `internal/store`
   takes the resolved DSN as-is and assembles nothing.
2. **Prod DSNs always `sslmode=require`.** Matches Aurora `rds.force_ssl=1`.
   `migrate.sh` and `GET /readyz` refuse non-`require` DSNs unless
   `CENTRAL_MEMORY_LOCAL_DEV=1` (compose sets it with `sslmode=disable`).
3. **JWT: `JWT_SECRET` canonical.** `CENTRAL_MEMORY_JWT_KEY` is accepted as a
   legacy fallback (`cmd/server` resolution order; `internal/server/auth.go`
   reads the legacy name). New docs/samples use `JWT_SECRET` only.
4. **`MIGRATIONS_DIR=/migrations` everywhere** (image path, compose, ECS,
   `/readyz` report). The old `/app/migrations` value is retired.
5. **Ports: server `8080`, daemon `7687`** — one value in binary defaults,
   Dockerfiles, compose, and docs. `DAEMON_ADDR`/`DAEMON_PORT` are dead
   (the daemon binary reads neither) and must not be set.
6. **Probes: `/healthz` = liveness, `/readyz` = readiness.** ALB target group
   uses `/healthz`; ECS container healthCheck uses `/readyz` (DSN + sslmode
   gate). Both are implemented by the server; the #112 "mismatch" is fixed
   by the endpoint existing, not by downgrading the check.

## Consequences

- `infra/terraform/secrets.tf` stores both forms (discrete keys + prebuilt
  `database_url` with `?sslmode=require`) for ONE credential: the dedicated
  app user `central_app` (bootstrapped once from the master per
  `deploy/rds-notes.md`), never the master username with a divergent password.
- `deploy/ecs-task.json` is a regenerated sample of the same contract.
- `deploy/docker-compose.yml` uses `DATABASE_URL` verbatim (dev) to exercise
  the primary path; ECS uses discrete parts to exercise the fallback.
- `implementation-plan.md` §1.9 dependency list and zero-deps claims are
  reconciled separately (docs/issues note for #95); this ADR owns only the
  deploy env surface.
