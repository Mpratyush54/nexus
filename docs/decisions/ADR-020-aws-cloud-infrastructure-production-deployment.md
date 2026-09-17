# ADR-020 — AWS Cloud Infrastructure & Production Deployment

- **ADR ID:** ADR-020-aws-cloud-infrastructure-production-deployment
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537
- **Issue:** #20 [Phase 6] AWS Cloud Infrastructure & Production Deployment Setup
- **Status:** Accepted

## Context

`implementation-plan.md` Locked Decisions fix the backend as **AWS Aurora
Serverless v2 (PostgreSQL + pgvector) + S3 + Secrets Manager**, with a hybrid
topology (local daemon → cloud Postgres). The "AWS Infrastructure" diagram
requires: Aurora (all 14 tables incl. `vector(1536)` columns), S3 (cold event
archive, episode attachments, exported snapshots), Secrets Manager (DB string,
JWT key), and EC2/ECS hosting for the central API + WebSocket hub. Phase 6
(§6.2) adds the retention rule: raw events older than 180 days go to S3
Glacier. Acceptance for #20: Aurora connects with pgvector; container boots +
migrates. Constraint: only `deploy/` (new), root Dockerfiles, `infra/`
Terraform, and `docs/` may be touched — no `internal/`, `migrations/`,
`web/`, or `cmd/` changes.

## Options Considered

1. **Terraform for infra + in-process migration-on-boot (chosen)** —
   `infra/terraform/*.tf` declares Aurora/S3/Secrets/ECS; the new
   `deploy/server-bootstrap` binary (stdlib + existing `internal/store`,
   `internal/server`) connects with retry, runs `store.RunMigrations` over the
   `migrations/*.up.sql` baked into the image, then serves. No new migration
   tooling, no sidecars.
2. **External migrate sidecar / init-container running psql** — would need a
   Postgres client in the image and duplicates `store.RunMigrations` logic
   that already exists, is tested (`db_test.go`), and owns ordering semantics.
   Rejected: two migration runners inevitably diverge.
3. **CDK instead of Terraform** — equally valid declaratively, but Terraform
   HCL is reviewable without a build toolchain and the team has no CDK
   constructs yet. Rejected: heavier bootstrap for identical resources.
4. **Bake migrated schema into a custom DB image / snapshot** — breaks the
   forward-only migration contract (`RunMigrations` applies `001_*…005_*` in
   order on every boot; a second replica booting later must converge to the
   same schema). Rejected: boot-time migrate is idempotent per-file and is the
   single source of truth.

## Decision

- `Dockerfile.server` (multi-stage `golang:1.26-alpine` → `alpine:3.21`,
  CGO-free static binary, non-root `appuser`): builds
  `deploy/server-bootstrap`, copies `migrations/` to `/app/migrations`.
- `Dockerfile.daemon` (Alpine + `git`, non-root): builds
  `deploy/daemon-bootstrap`; `git` must be in PATH for `/git/*` routes and
  allowlisted `git` commands.
- `deploy/server-bootstrap/main.go`: `DATABASE_URL` or discrete `DB_*` env →
  `store.Connect` with 3-minute retry (Aurora wake) →
  `store.RunMigrations` → `server.New` + open `GET /readyz` (pool health +
  migration count) → serve `$PORT`. Secrets arrive only as env vars injected
  by ECS from Secrets Manager.
- `deploy/daemon-bootstrap/main.go`: `daemon.New(WORKSPACE_ROOT,
  CENTRAL_SERVER_URL, DAEMON_ADDR)` → `Start` → `Register` (soft-fail) →
  30s heartbeat loop → signal-drain shutdown.
- Terraform: Aurora Serverless v2 cluster (PG16, `aurora-postgresql16`
  parameter group for pgvector, `rds.force_ssl=1`, KMS encryption, 0.5–4
  ACU) + `db.serverless` writer; S3 archive bucket (180-day Glacier
  transition per §6.2) + snapshots bucket (versioned, SSE-S3, public-blocked);
  two Secrets Manager secrets (`random_password`-generated, zero literals);
  ECS/Fargate (512 CPU / 1024 MiB — smallest sane pairing for REST+WS+pgx),
  ALB with `/healthz` checks, container `readyz` healthcheck with 120s
  startPeriod for Aurora wake + migrations, least-privilege secret-read IAM.
- `deploy/docker-compose.yml`: local prod-shape stack on
  `pgvector/pgvector:pg16` for acceptance verification without AWS creds.

## Why (Rationale)

This is the only option that meets every acceptance criterion inside the
ownership boundary: Aurora+pgvector is declared (cluster + PG16 param group)
and exercised (001's `CREATE EXTENSION "vector"` runs through the real
`RunMigrations` path at boot, locally verifiable via compose); container
boots+migrates holds by construction (migrations precede `ListenAndServe`,
`/readyz` reports the applied count, and the ECS healthcheck gates ALB
registration on it); Secrets Manager refs are ARN-based `secrets` blocks, so
no credential ever touches git, the image, or logs. Reusing
`store.RunMigrations`/`DefaultConfig`/`daemon.New`/`server.New` keeps all
runtime behavior inside already-reviewed, already-tested packages — `go build
./...` stays green with purely additive code (`deploy/*` only), and the
`cmd/`/`internal/`/`migrations/`/`web/` ban is fully respected.

## Consequences

- Follow-up (store/server owners): replace `stubStore` with the real
  Postgres-backed `server.Store` adapter (memory/episode vector search).
  Until then data routes fail closed (500 + "pending" message); `/healthz`,
  `/readyz`, `/auth/login` shape are live.
- Follow-up: CI builds/pushes `Dockerfile.server` to ECR (`var.server_image`),
  real ACM cert for `var.api_domain`, remote state backend, `terraform apply`
  with credentials (apply was impossible here — no AWS creds; see ISSUE-20).
- Follow-up: Aurora auto-pause/scale policy tuning and S3 cold-archive writer
  job (§6.2 warm→cold transition is declared, not yet implemented).

## Alternatives Rejected

Sidecar/psql migration (duplicates tested runner), CDK (heavier bootstrap,
same resources), baked-schema images (breaks forward-only convergence) — see
Options Considered above.
