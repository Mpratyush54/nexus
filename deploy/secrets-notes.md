# Secrets notes — Secrets Manager wiring (template)

> Templates/notes only. No live secret creation.

## Canonical env contract (single source of truth — issue #112)

| Secret name (TF) | Key(s) | Consumed as | Used by |
|---|---|---|---|
| `central-memory/db-app` | `username`,`password`,`host`,`port`,`dbname` | `DB_USER`…`DB_NAME` (+ `DB_SSLMODE=require` env) → DSN assembled identically by `migrate.sh` and `cmd/server` | server, migrate.sh |
| `central-memory/db-app` | `database_url` (same credential, `?sslmode=require` prebuilt) | `DATABASE_URL` verbatim | server, migrate.sh, `ecs-task.json` sample |
| `central-memory/jwt` | `signing_key` | **`JWT_SECRET`** (canonical; `CENTRAL_MEMORY_JWT_KEY` still accepted as legacy fallback) | server |
| (plain env / optional) | — | `CENTRAL_EMBEDDING_PROVIDER` (`openai`\|`ollama`\|`hash`), `CENTRAL_EMBEDDING_API_KEY`, `CENTRAL_EMBEDDING_MODEL`, `CENTRAL_EMBEDDING_ENDPOINT` | server, `mem mcp` (issue #165; defaults to hash) |
| `openrouter` | `api_key` | **`OPENROUTER_API_KEY`** — server-side harvest extraction; never shipped to clients | server |
| (plain env / optional) | — | `OPENROUTER_MODEL` (default `nvidia/nemotron-3.5-lightning:free`; avoid `openrouter/free` which can route to safety-only models), `OPENROUTER_BASE_URL`, `OPENROUTER_HTTP_REFERER`, `OPENROUTER_APP_TITLE` | server |
| `central-memory/smtp` | `host`,`port`,`username`,`password`,`from` | **`SMTP_HOST`**, **`SMTP_PORT`**, **`SMTP_USER`**, **`SMTP_PASSWORD`**, **`MAIL_FROM`** — signup + forgot-password OTP mail via Brevo or SendGrid | server |

### Mail (Brevo / SendGrid → `@pratyushes.dev`)

From address is always **`Nexus <noreply@pratyushes.dev>`** (domain must be verified in Brevo/SendGrid).

Seeded secret shape (Terraform creates `central-memory/smtp`; **you set `password`**):

| Key | Brevo (default) | SendGrid |
|---|---|---|
| `host` | `smtp-relay.brevo.com` | `smtp.sendgrid.net` |
| `port` | `587` | `587` |
| `username` | `mpratyush54@gmail.com` | `apikey` |
| `password` | Brevo SMTP key | SendGrid API key |
| `from` | `Nexus <noreply@pratyushes.dev>` | same |

```bash
# After TF creates the secret, put the real SMTP password (never commit it):
aws secretsmanager put-secret-value \
  --secret-id central-memory/smtp \
  --secret-string '{"host":"smtp-relay.brevo.com","port":"587","username":"mpratyush54@gmail.com","password":"<SMTP_KEY>","from":"Nexus <noreply@pratyushes.dev>"}'
```

Then force a new ECS deployment so tasks pick up the secret. Without `password`, OTP mail stays unavailable in prod (`email delivery is not configured`). Local: `CENTRAL_MEMORY_LOCAL_DEV=1` still returns `dev_code`.


Free OpenRouter models are rate-limited (~20 RPM / ~50 RPD without credits). The API throttles LLM extract to about one call per project per 5 minutes and falls back to a tightened heuristic when the key is unset or the call fails.

Rules:

- `DATABASE_URL` wins when set; otherwise the discrete `DB_*` parts are
  assembled into the identical DSN. Both describe the same credential
  (`central_app`, never the master user — issue #127).
- Prod DSNs always carry `sslmode=require` (matches `rds.force_ssl=1`).
  `sslmode=disable` exists only for local compose with
  `CENTRAL_MEMORY_LOCAL_DEV=1`; `migrate.sh` and `/readyz` refuse anything
  else outside local-dev.
- `MIGRATIONS_DIR=/migrations` everywhere (image, compose, ECS, `/readyz`).

## Create (manual, one-time — Terraform owns this; example for operators)

```bash
# The db-app secret (dedicated app user bootstrapped per deploy/rds-notes.md):
aws secretsmanager create-secret \
  --name central-memory/db-app \
  --secret-string '{"username":"central_app","password":"<generated>","host":"<cluster-endpoint>","port":5432,"dbname":"central_memory","database_url":"postgres://central_app:<generated>@<cluster-endpoint>:5432/central_memory?sslmode=require"}'

aws secretsmanager create-secret \
  --name central-memory/jwt \
  --secret-string '{"signing_key":"<32+ random bytes, base64>"}'

# Mail SMTP (if not created by Terraform) — replace <SMTP_KEY> with Brevo/SendGrid secret:
aws secretsmanager create-secret \
  --name central-memory/smtp \
  --secret-string '{"host":"smtp-relay.brevo.com","port":"587","username":"mpratyush54@gmail.com","password":"<SMTP_KEY>","from":"Nexus <noreply@pratyushes.dev>"}'
```

Generate the JWT key locally, never commit it:

```bash
# PowerShell:
# [Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Max 256 }))
```

## ECS wiring

- The task execution role needs `secretsmanager:GetSecretValue` on db-app, jwt, openrouter, and **smtp** ARNs.
- `deploy/ecs-task.json` injects them via `containerDefinitions[].secrets`
  (`valueFrom` = `secret-arn:key:version-stage:version-id`); containers see
  plain env vars, secrets never land in the image or task JSON.
- Rotation: rotate `db-app` in Secrets Manager, then force a new
  deployment (`aws ecs update-service --force-new-deployment`). Stagger: Aurora
  credential rotation briefly invalidates old passwords — prefer a maintenance
  window until RDS-managed rotation is configured.

## Local dev

- Compose sets `DATABASE_URL` (dev, `sslmode=disable`) +
  `CENTRAL_MEMORY_LOCAL_DEV=1` + `JWT_SECRET` as plain env vars. Production
  must always come from Secrets Manager.
