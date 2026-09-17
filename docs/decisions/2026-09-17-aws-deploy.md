# AWS Production Deployment — Phase 6 Decisions

Date: 2026-09-17
Scope: nexus issue #20 ([Phase 6] AWS Cloud Infrastructure & Production
Deployment) / implementation-plan.md Locked Decisions + §6.2 + AWS
Infrastructure diagram.
Status: templates/notes only — `deploy/` holds Dockerfiles, a sample ECS task
definition, and operator notes. No `terraform apply`, no Go code changes.

## Decision 1 — Why Aurora Serverless v2 (Postgres 16 + pgvector)

Why:

- The plan locks "Aurora scales to zero for early stage". Serverless v2 scales
  down to 0.5 ACU at idle, so the sync/merge point costs near-minimum while
  this is a nights-and-weekends project, yet bursts for harvester backfills
  and end-of-session deep-analysis write spikes without re-provisioning.
- Postgres 16 ships the `vector` extension on Aurora, which the schema already
  assumes (`migrations/001_initial.up.sql`: `CREATE EXTENSION "vector"`,
  `embedding vector(1536)`, `ivfflat` indexes on `memory_items`/`episodes`).
  Staying on managed Postgres avoids operating a separate vector store and
  keeps vector + full-text fallback in one query path (§1.5).
- Serverless v2 (not v1) because v1 lacks pgvector support and has
  connection-proxy quirks with long-lived WebSocket fan-out (§3.2); v2 is a
  real Postgres engine with Data API-free `pgx` connections, matching the
  locked `pgx/v5 + pgvector-go` stack.

Consequence: 0.5 ACU floor means "scales to zero" is really "scales to
minimum" — not $0. Cold starts add ~seconds after idle; acceptable for a
memory sync plane, not for sub-second presence. Revisit provisioned Aurora
I/O-Optimized only with measured events/sec pressure (§6.5 metrics first).

## Decision 2 — Why S3 cold archive (event retention §6.2)

Why:

- The retention table already tiers 180+ day events to "S3 Glacier" with
  payloads dropped and episodes preserved. S3 (Standard → lifecycle to
  Glacier) is the direct implementation of that row: raw `events` payloads and
  exported memory snapshots go to versioned buckets; Postgres keeps hot/warm
  queryable windows small and cheap.
- Episode rows stay in Aurora (they are the distilled, searchable record),
  so cold archiving loses no retrieval quality — `episode_search` never needs
  the raw 180-day-old tool-call stdout.
- S3 also covers the plan diagram's "episode attachments + exported memory
  snapshots" without a second storage system.

Consequence: restore-from-Glacier is hours, not seconds — by design (cold
means audit/debug only). Lifecycle rules + bucket versioning must be codified
when Terraform/CDK lands; until then this is intent, not infrastructure.

## Decision 3 — Why Secrets Manager (DB string + JWT key)

Why:

- Two secrets, both already load-bearing: `DATABASE_URL` (Aurora connection
  string for `pgx` pool + `migrate.sh`) and `CENTRAL_MEMORY_JWT_KEY` (signing
  key for the `internal/server/auth.go` HMAC stub, future `jwt/v5`).
- Secrets Manager over env-baked config because ECS injects via
  `containerDefinitions[].secrets` (`valueFrom` ARNs in
  `deploy/ecs-task.json`): secrets never land in the image, task JSON, or git.
  Rotation is a console/API call + `update-service --force-new-deployment`,
  not a rebuild.
- Smallest viable surface: exactly the two secrets in
  `deploy/secrets-notes.md`. Daemon tokens stay local files (`0600` per §1.3);
  user LLM keys stay on user devices (Memory Processor is local, §1.3) — none
  of these ever enter Secrets Manager.

Consequence: task execution role needs `secretsmanager:GetSecretValue`;
  local dev keeps plain-env fallback. Rotation briefly invalidates DB
  passwords — schedule outside active sessions until RDS-managed rotation.

## Decision 4 — Why ECS/Fargate (not EC2, not Lambda)

Why:

- The plan diagram says "EC2 / ECS" for the central server + WebSocket hub.
  Fargate resolves the ambiguity toward serverless: no instances to patch for
  a single Go HTTP+WebSocket process, `awsvpc` networking drops it into the
  same private VPC as Aurora, and service autoscaling tracks concurrent
  daemon connections.
- Lambda is wrong for this workload: the WebSocket hub (§3.2) holds
  long-lived connections with per-client backpressure (§6.4) — Lambda's
  request-scoped execution fights that model, while Fargate tasks hold
  connections for hours like the design assumes.
- Raw EC2 is kept as the escape hatch (daemon dogfooding, GPU-adjacent
  experiments), not the default: Fargate's per-second billing matches the
  same early-stage economics as Serverless v2.

Consequence: Fargate tasks are cattle — `migrate.sh` must be idempotent
  (`CREATE EXTENSION IF NOT EXISTS`, ordered `*.up.sql`) because every deploy
  re-runs it. Sticky-session ALB target groups are required once >1 task
  serves WebSockets (fan-out correctness, §3.2).

## Decision 5 — Why Docker (two images)

Why:

- Reproducible artifact for both binaries in the hybrid topology: the cloud
  `central-server` (must boot identically on Fargate and in integration-test
  Compose) and the local `workspace-daemon` (containerized for dev/test
  parity even though production runs it as a native binary via
  `internal/platform` installers).
- Multi-stage (`golang:1.26-alpine` → distroless `nonroot`): matches the
  locked Go 1.26 toolchain, keeps the runtime image to the static binary +
  migrations, and runs as non-root by default.
- `migrate.sh` as ENTRYPOINT (not baked-in `RUN`) so the container that
  serves traffic is the container that migrated the schema — no separate
  migration job to forget, satisfying "runs migrations automatically on
  launch".

Consequence: `cmd/server` + `cmd/daemon` entrypoints do not exist yet (only
  `cmd/nexus` does), so both Dockerfiles fail fast until Phase 1 lands them —
  intentional, flagged in `deploy/README.md`. Daemon image exposes the same
  sandbox/allowlist behavior as the native binary; no Docker-specific auth
  bypass.

## Verification (templates only — nothing applied)

- `deploy/` contains exactly: `Dockerfile.server`, `Dockerfile.daemon`,
  `README.md`, `ecs-task.json` (sample), `rds-notes.md`, `secrets-notes.md`,
  `migrate.sh`. No Go files touched, no `terraform apply`.
- Acceptance mapping: pgvector enable steps → `rds-notes.md` smoke query;
  boot-migration → `migrate.sh` + `Dockerfile.server` ENTRYPOINT;
  Secrets wiring → `secrets-notes.md` + `ecs-task.json` secrets block.
