# RDS notes — Aurora Serverless v2 (Postgres 16) + pgvector (template)

> Templates/notes only. No `terraform apply`, no live changes.

## Target

- Engine: Aurora PostgreSQL 16.x, Serverless v2.
- Why Serverless v2: scales to ~0.5 ACU at idle (early stage, near-zero traffic
  most of the day), bursts for harvester backfills / deep-analysis passes.
  See `docs/decisions/2026-09-17-aws-deploy.md`.

## One-time setup checklist

1. Create the cluster in a private VPC (2+ AZs), Postgres 16 or later.
2. Pre-provision extensions as the RDS **master user** (issue #45): on
   Aurora, `CREATE EXTENSION` requires rds_superuser, which the app user
   must NOT have. Connect once as master and run:
   ```sql
   CREATE EXTENSION IF NOT EXISTS "pgcrypto";
   CREATE EXTENSION IF NOT EXISTS "vector";
   ```
   `migrations/001_initial.up.sql` repeats the same statements with
   `IF NOT EXISTS`, so first boot is a safe no-op after this step — the
   app user only needs CONNECT + DML/DDL on its own schema, never
   superuser.
3. First boot runs `migrations/001_initial.up.sql`, which executes:
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
4. Store the connection string in Secrets Manager (see `secrets-notes.md`);
   never bake it into the task definition or image.

## Verifying pgvector (acceptance: "Aurora connects with pgvector enabled")

```sql
-- From any client using the Secrets Manager DATABASE_URL:
CREATE TEMP TABLE vector_smoke (id serial PRIMARY KEY, embedding vector(3));
INSERT INTO vector_smoke (embedding) VALUES ('[1,2,3]'), ('[1,2,4]');
SELECT id FROM vector_smoke ORDER BY embedding <=> '[1,2,3]' LIMIT 1;
-- expected: 1
```

## Gotchas

- `ivfflat` indexes (used in `001_initial.up.sql`) require the table to have
  rows before `CREATE INDEX ... WITH (lists = N)` is efficient; for fresh
  deploys this is fine — the index builds as data arrives.
- Serverless v2 pause-to-zero is NOT supported (min 0.5 ACU); "scales to zero"
  in the plan means "scales to minimum", not $0.
- Major-version upgrades (16 → 17): snapshot first, test `vector` extension
  compatibility in a restored clone.
