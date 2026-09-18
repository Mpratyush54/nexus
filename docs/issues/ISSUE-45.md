# ISSUE-45 — Infra Hardening

- **Status:** Done (tf + daemon + compose + docs; `terraform apply` never run — no creds, same posture as #20)
- **Scope:** `infra/terraform/{variables,aurora,ecs,backend}.tf`, `versions.tf` (comment),
  `internal/daemon/auth.go`, `cmd/daemon/main.go`, `deploy/docker-compose.yml`,
  `deploy/rds-notes.md`, `docs/decisions/ADR-045-infra-hardening.md` ONLY.

## What was built

- ALB on public subnets (`public_subnet_ids` + `effective_public_subnets`;
  tasks/DB stay private) with explicit `internal = false`.
- `:80 → :443` 301 listener + SG 80-ingress (redirect-only).
- `$DAEMON_TOKEN_FILE` token relocation; compose points it at
  `/tmp/daemon.token`, workspace mount tightened `:rw` → `:ro`.
- `backend.tf` partial-S3 example — DROPPED at merge (2026-09-18): master
  keeps remote state as comments in `versions.tf` so `terraform validate`
  stays credential-free in CI; an active `backend "s3" {}` block would
  break that. The `-backend-config` pattern is documented in `versions.tf`.
- RDS notes: superseded at merge by master's corrected runbook (#127:
  parameter group REQUIRED, `central_app` user bootstrap, Secrets Manager
  wiring) — kept master's version.
- Subnet split / redirect listener / `internal = false` / `public_subnet_ids`:
  superseded at merge by master's fuller public/private split
  (`effective_public_subnets` + `effective_private_subnets`, #127/#128) —
  kept master's version.

## Verification

- `go build ./...` clean; `go vet ./internal/...` clean;
  `go test -count=1 ./internal/daemon/...` green (incl. new
  `TestTokenFileEnvOverride`).
- HCL reviewed by hand (`terraform`/`docker` unavailable here); `terraform
  validate` + compose up left for an env with creds/docker.
