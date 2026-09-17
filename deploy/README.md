# deploy/ — production containers + local verification (issue #20)

| Path | Purpose |
|---|---|
| `server-bootstrap/main.go` | Server entrypoint: connect (retry) → `store.RunMigrations` → serve `internal/server` + `/readyz`. Data routes fail closed until the Postgres adapter follow-up lands. |
| `daemon-bootstrap/main.go` | Daemon entrypoint: `daemon.New` → serve → register → 30s heartbeat loop. |
| `docker-compose.yml` | Local prod-shape stack: Postgres 16 + pgvector, server, daemon. |
| `../Dockerfile.server` | Multi-stage server image; ships `migrations/` for boot-time migrate. |
| `../Dockerfile.daemon` | Multi-stage daemon image (Alpine + git for `/git/*` routes). |
| `../infra/terraform/` | AWS: Aurora Serverless v2 (PG16 + pgvector), S3 archive/snapshot buckets, Secrets Manager, ECS/Fargate + ALB. |

No secrets live here. Production config is injected as environment variables
from Secrets Manager by ECS (`infra/terraform/ecs.tf` `secrets` block);
`docker-compose.yml` values are dev-only placeholders.
