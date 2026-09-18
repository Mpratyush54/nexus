# RDS notes — Aurora Serverless v2 (Postgres 16) + pgvector (template)

> Templates/notes only. No `terraform apply`, no live changes.

## Target

- Engine: Aurora PostgreSQL **16.6** (pinned full version in
  `infra/terraform/variables.tf:aurora_engine_version` — Aurora rejects bare
  `16`; issue #127). Upgrade path: snapshot → restore a clone on the new
  version → verify `vector` extension → `terraform apply` the version bump.
- Why Serverless v2: scales to ~0.5 ACU at idle (early stage, near-zero traffic
  most of the day), bursts for harvester backfills / deep-analysis passes.
  See `docs/decisions/2026-09-17-aws-deploy.md`.

## One-time setup checklist

1. Create the cluster in private VPC subnets (2+ AZs) via
   `infra/terraform` (ALB alone lives on public subnets; tasks + DB are
   private — issue #45).
2. Parameter group: the Terraform `aws_rds_cluster_parameter_group.pgvector`
   (`aurora-postgresql16` family, `rds.force_ssl=1`) is **REQUIRED** — it is
   what makes the `vector` extension installable and forces SSL. (An older
   revision of this note said no custom group was needed; that was wrong and
   is corrected here per #127. Do not skip the group.)
3. Master bootstrap (REQUIRED before first boot; issue #45 "ext provisioning"
   + #127 "app user never created"). As the RDS master user (password in the
   RDS-managed master secret):
   ```sql
   -- Extension privilege for the migration user (001 runs CREATE EXTENSION):
   GRANT rds_superuser TO central;            -- or narrower: GRANT CREATE ON DATABASE central_memory TO central_app;
   -- Dedicated app user (password = db-app secret in Secrets Manager):
   CREATE USER central_app WITH PASSWORD '<db-app secret password>';
   GRANT CONNECT ON DATABASE central_memory TO central_app;
   GRANT CREATE ON DATABASE central_memory TO central_app;  -- needed once for CREATE EXTENSION
   GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO central_app;
   ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO central_app;
   ```
   The app NEVER logs in as the master user; the db-app secret holds
   `central_app` + its password (see `secrets-notes.md`). Terraform cannot do
   this step (no provisioner by design) — it is an operator runbook step.
4. First boot runs `migrations/001_initial.up.sql`, which executes:
   ```sql
   CREATE EXTENSION IF NOT EXISTS "pgcrypto";
   CREATE EXTENSION IF NOT EXISTS "vector";
   ```
   Verify after first boot:
   ```sql
   SELECT * FROM pg_extension WHERE extname IN ('vector', 'pgcrypto');
   -- expected: two rows
   SELECT * FROM pg_available_extensions WHERE name = 'vector';
   ```
5. Store the connection string in Secrets Manager (see `secrets-notes.md`);
   never bake it into the task definition or image.

## SSL (`rds.force_ssl=1` + reboot note)

- The parameter group sets `rds.force_ssl=1` with `apply_method=pending-reboot`
  (issue #127): the cluster needs a reboot (or failover) after the group
  attaches before SSL is actually forced. Until then plaintext may still be
  accepted despite the intent.
- After first apply: reboot the writer (or wait for the maintenance window),
  then verify:
  ```sql
  SHOW rds.force_ssl;  -- expected: on
  ```
- Defense in depth (independent of the reboot timing): `migrate.sh` and
  `cmd/server` `/readyz` refuse a non-`require` DSN outside local-dev, and
  every stored secret pins `?sslmode=require`. Prod traffic is `require` by
  construction even if the reboot is pending.

## Verifying pgvector (acceptance: "Aurora connects with pgvector enabled")

```sql
-- From any client using the Secrets Manager DATABASE_URL:
CREATE TEMP TABLE vector_smoke (id serial PRIMARY KEY, embedding vector(3));
INSERT INTO vector_smoke (embedding) VALUES ('[1,2,3]'), ('[1,2,4]');
SELECT id FROM vector_smoke ORDER BY embedding <=> '[1,2,3]' LIMIT 1;
-- expected: 1
```

## DSN/SSL wiring (where it lives — issue #127)

There is no `deploy/server-bootstrap` Go binary: the DSN builder lives in
**two** places that implement the same contract (documented once in
`secrets-notes.md`):

- `deploy/migrate.sh` (shell): `DATABASE_URL` verbatim, else assembles
  `DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD` + `DB_SSLMODE` (default
  `require`).
- `cmd/server/main.go` `resolveDatabaseURL` (Go): identical precedence.
- `internal/store/db.go` `NewPostgresStore` takes the resolved DSN as-is
  (`pgxpool.ParseConfig`); it does not assemble parts and does not need to.

## Gotchas

- `ivfflat` indexes (used in `001_initial.up.sql`) require the table to have
  rows before `CREATE INDEX ... WITH (lists = N)` is efficient; for fresh
  deploys this is fine — the index builds as data arrives.
- Serverless v2 pause-to-zero is NOT supported (min 0.5 ACU); "scales to zero"
  in the plan means "scales to minimum", not $0.
- Major-version upgrades (16 → 17): snapshot first, test `vector` extension
  compatibility in a restored clone.
- Destroy friction: `skip_final_snapshot=false` (default) keeps a final
  snapshot named `<project>-final` on destroy; set
  `-var skip_final_snapshot=true` for ephemeral stacks (issue #127).
