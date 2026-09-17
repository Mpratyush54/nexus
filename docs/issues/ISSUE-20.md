# ISSUE-20 — AWS Cloud Infrastructure & Production Deployment Setup

- **Issue:** #20 — [Phase 6] AWS Cloud Infrastructure & Production Deployment Setup
- **Status:** Done (implementation + docs; cloud apply + image push pending creds/CI)
- **Assignee:** ParthKhandelwal537
- **Scope constraint:** ONLY `deploy/` (new), root `Dockerfile.server` /
  `Dockerfile.daemon` (new), `infra/terraform/*.tf` (new), `docs/`. Did NOT
  touch `internal/`, `migrations/`, `web/`, `cmd/` (no `cmd/` exists in tree;
  server/daemon entrypoints are additive mains under `deploy/`).
- **Plan ref:** `implementation-plan.md` Locked Decisions (Aurora Serverless v2
  + S3 + Secrets Manager), "AWS Infrastructure" diagram, §§1.1 (pgvector
  schema), 1.3 (daemon), 1.8 (server), 6.2 (retention). Read first per brief;
  verified `migrations/001_initial.up.sql` (`CREATE EXTENSION "vector"` +
  `vector(1536)` columns) before starting.

## What was built

| File | Contents |
|---|---|
| `Dockerfile.server` | Multi-stage (`golang:1.26-alpine` → `alpine:3.21`, static `CGO_ENABLED=0`, non-root `appuser`); builds `deploy/server-bootstrap`; bakes `migrations/` → `/app/migrations` for boot-time migrate |
| `Dockerfile.daemon` | Same shape + `git` in runtime PATH (required by `/git/*` routes and allowlisted `git` commands); `/workspace` mount owned by `appuser` (0600 token writes work rootless) |
| `deploy/server-bootstrap/main.go` | Env config (`DATABASE_URL` or discrete `DB_*`) → `store.Connect` with 3-min retry (Aurora wake) → `store.RunMigrations` (migrate BEFORE `ListenAndServe`) → `server.New` + open `GET /readyz` (pool health + applied count) on `$PORT` |
| `deploy/daemon-bootstrap/main.go` | `daemon.New` → `Start` → `Register` (soft-fail) → 30s heartbeat loop → signal-drain shutdown; empty `CENTRAL_SERVER_URL` = local-only mode |
| `deploy/docker-compose.yml` | Local prod-shape stack (`pgvector/pgvector:pg16` + server + daemon, healthchecks); dev-only placeholder secrets |
| `deploy/README.md` | Map of `deploy/` + secret-injection rule |
| `infra/terraform/versions.tf` | `terraform >= 1.9`, `aws ~> 5.0`, `random ~> 3.0`; backend left to `-backend-config` at apply |
| `infra/terraform/variables.tf` | Region/project/VPC/subnets, Aurora 0.5–4 ACU, Fargate 512/1024, image + domain |
| `infra/terraform/aurora.tf` | Serverless v2 PG16 cluster + `db.serverless` writer, `aurora-postgresql16` param group (`rds.force_ssl=1`), KMS encryption, deletion protection, 7d backups |
| `infra/terraform/s3.tf` | Archive + snapshot buckets: versioned, SSE-S3, public-blocked, 180-day Glacier transition (plan §6.2) |
| `infra/terraform/secrets.tf` | `random_password`-generated DB + JWT secrets; zero literals |
| `infra/terraform/ecs.tf` | Cluster (Container Insights), ALB (`/healthz` checks) + HTTPS listener, task def with Secrets-Manager `secrets` refs, `readyz` healthcheck (120s startPeriod), least-privilege secret-read IAM, service desired 1 |
| `infra/terraform/outputs.tf` | Endpoint, secret ARNs (sensitive), ALB DNS, bucket names |
| `docs/decisions/ADR-020-aws-cloud-infrastructure-production-deployment.md` | Why-mandatory ADR (Terraform+in-process-migrate choice, option rejections, stub-store honesty) |
| `docs/issues/ISSUE-20.md` | This file |

## Decisions (see ADR-020 for rationale)

1. In-process migration-on-boot reusing `store.RunMigrations` — no psql
   sidecar, no second runner to diverge; forward-only `001_*…005_*` ordering kept.
2. `deploy/*-bootstrap` additive mains (stdlib + existing packages) instead of
   touching `cmd/`/`internal/` — server needs a `server.Store`, whose full
   Postgres adapter is another issue's follow-up, so data routes fail closed
   (500 + "pending") while `/healthz`/`/readyz`/migrate are fully live.
3. ECS `secrets` (ARN + json-key `valueFrom`) for every credential; bootstrap
   accepts discrete `DB_*` parts so no composite connection string is stored.
4. Fargate 512/1024 (smallest sane pairing for REST+WS+pgx pool), Aurora
   0.5–4 ACU (engine-minimum near-zero), service count 1 (single boot-time
   migrator for v1).

## Verification (self-review checklist)

- [x] **pgvector param group:** `aws_rds_cluster_parameter_group.pgvector`
  (`family = "aurora-postgresql16"`) attached as
  `db_cluster_parameter_group_name`; `CREATE EXTENSION "vector"` arrives via
  migration 001 at boot (verified in `migrations/001_initial.up.sql`).
- [x] **Secrets Manager refs not hardcoded:** `grep`-clean by construction —
  no password/key/token literal in any new file; secrets are
  `random_password` → secret_version, consumed only via ECS `secrets`
  `valueFrom` ARNs. (`DB_SSLMODE=disable` appears once, in dev-only compose.)
- [x] **Migration-on-boot entrypoint:** `RunMigrations` return precedes
  `ListenAndServe`; failure `log.Fatal`s (crash-loop, never serves stale
  schema); applied files logged + exposed on `/readyz`.
- [x] **Fargate CPU/mem sane:** 512 CPU / 1024 MiB, valid Fargate pairing for
  Go REST + WebSocket fan-out + pgx pool (16 conns).
- [x] **No AWS creds available — noted:** `terraform` and `docker` are not
  installed in this environment and no AWS credentials exist, so `terraform
  validate/apply` and image builds were NOT run. `.tf` was self-reviewed
  (fixed: dropped a possibly-invalid instance arg, added explicit
  `subnet_ids` for custom VPCs). Cloud apply + ECR push are follow-ups.
- [x] **`go build ./...` passes (additive Go only):** `BUILD_OK`; `go vet
  ./deploy/...` → `VET_OK`; `gofmt -l deploy/` → clean; full `go test ./...`
  → `TEST_OK` (all packages ok, zero regressions; new packages have no test
  files by design — they are thin env-wiring over tested packages).
- [x] **Ownership:** `git status` shows my new files only under `deploy/`,
  `infra/`, root `Dockerfile.*`, `docs/`; parallel agents' files
  (`ADR-022/024/026`, `ISSUE-22`, `internal/governance|platform|steer`) untouched.

## Follow-ups (not this issue)

- Store/server owners: Postgres-backed `server.Store` adapter to replace
  `stubStore` (memory/episode vector search wiring).
- CI: build/push `Dockerfile.server` → ECR (`var.server_image`); real ACM
  cert (`var.api_domain`); remote state backend; `terraform apply` with creds.
- Runtime tuning: Aurora auto-pause/scale policy, S3 warm→cold archive writer
  job (§6.2 declares the bucket lifecycle; the mover is not implemented).
- Daemon is local-first by design — no Fargate definition shipped for it
  (compose covers containerized verification).
