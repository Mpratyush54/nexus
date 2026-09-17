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
  `/tmp/daemon.token` (fixes `:ro` token-mint failure).
- `backend.tf` partial-S3 example with bootstrap + `-backend-config` line.
- RDS notes: master pre-provisions both extensions; app user needs no superuser.

## Verification

- `go build ./...` clean; `go vet ./internal/...` clean;
  `go test -count=1 ./internal/daemon/...` green (incl. new
  `TestTokenFileEnvOverride`).
- HCL reviewed by hand (`terraform`/`docker` unavailable here); `terraform
  validate` + compose up left for an env with creds/docker.
