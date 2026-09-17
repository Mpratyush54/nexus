# ADR-045 — Infra Hardening: Subnets, Redirect, Compose, Backend, Extensions

- **ADR ID:** ADR-045-infra-hardening
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537
- **Issue:** #45 infra hardening (subnets, HTTP redirect, compose :ro, backend, ext provisioning)
- **Status:** Accepted

## Context

The issue-#20 AWS stack applies in theory but has five deployment-time
holes, all found by audit: the internet-facing ALB sits on the same
(private) subnets as the tasks; port 80 has no listener so HTTP clients
get connection-refused instead of a redirect; the compose daemon mounts
the workspace `:ro` while the daemon mints its token file under that
root; there is no remote-state example; and `CREATE EXTENSION` on Aurora
needs rds_superuser, which the app user must never hold.

## Decisions

1. **Subnet split** (`variables.tf`, `aurora.tf`, `ecs.tf`): new
   `public_subnet_ids` input; `effective_public_subnets` local (explicit
   public wins, else falls back to app subnets — correct for the default
   VPC). ALB uses public + explicit `internal = false`; tasks and Aurora
   stay on the private `effective_subnets`. Custom VPCs MUST set both.
2. **:80 redirect** (`ecs.tf`): `aws_lb_listener.http_redirect` 301s
   everything to `https://:443`; the ALB SG gains an 80-ingress scoped by
   comment to redirect-only. Nothing is ever served on port 80.
3. **Token relocation** (`internal/daemon/auth.go`, `cmd/daemon/main.go`,
   `deploy/docker-compose.yml`): `$DAEMON_TOKEN_FILE` overrides the token
   path (`ResolveTokenFile`; `Save/Load/EnsureToken{To,From,At}` are the
   path-parameterized cores, old root-based wrappers kept). Compose sets
   `DAEMON_TOKEN_FILE=/tmp/daemon.token` — writable, ephemeral per boot
   (documented; production mounts a persistent state volume there).
4. **Backend example** (`infra/terraform/backend.tf`): partial
   `backend "s3" {}` + commented bucket/table bootstrap and
   `-backend-config` init line. Plain init keeps local state for dev.
5. **Extension pre-provision** (`deploy/rds-notes.md`): master user runs
   both `CREATE EXTENSION IF NOT EXISTS` once; app user needs no
   superuser since 001 repeats the statements idempotently.

## Consequences

- `terraform validate` passes unchanged in shape (no new providers);
  apply still never run here (no creds) — same verification posture as
  issue #20.
- Daemon token behavior is unchanged when the env is unset (existing
  tests untouched; new `TestTokenFileEnvOverride` pins the override).
