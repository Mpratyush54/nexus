# ISSUE-95 — Reconcile implementation-plan.md with Phase 6+ reality

- **Status:** Partial (docs-side reconciliation landed here; the root
  `implementation-plan.md` edit itself is out of scope for deploy fixes and
  left to the docs owner — this file is the authoritative delta so the plan
  can be refreshed mechanically).
- **Scope:** `docs/issues/ISSUE-95.md` (this file) + `deploy/README.md` +
  `web/README.md` ONLY. Root `implementation-plan.md`, `go.mod`,
  `internal/`, `migrations/`, `cmd/` (except `cmd/server/main.go` per the
  deploy contract) untouched.

## Delta: plan snapshot vs current tree (verified 2026-09-18)

1. **Migrations `006`, `007`, `008` exist; plan stops at `005`.**
   `migrations/` contains `006_session_fks`, `007_branch_upkeep`,
   `008_processor_election` (+ `.down.sql` each). Plan "Target Directory
   Structure" lists only `001`–`005`. Refresh must extend the migration
   table and the RunMigrations ordering notes (001–005 idempotent
   re-execution + 006–008).
2. **`internal/` packages beyond the plan:** `security`, `governance`,
   `handoff`, `steering` (plus `migrate`, `branches`, task stores) have no
   plan target-tree entry. Mark implemented (they ship with audit tests);
   plan refresh should add them under Phase 6 (hardening/governance).
3. **§1.9 dependency list vs reality:**
   - `github.com/jackc/pgx/v5` + `github.com/pgvector/pgvector-go` — LANDED
     (in `go.mod`, as planned).
   - `nhooyr.io/websocket` — INTENTIONALLY SUBSTITUTED with a stdlib-only
     hub (`internal/server/ws.go`: net/http Hijack + minimal RFC 6455;
     rationale in code header + `docs/decisions/2026-09-17-sessions-ws.md`
     family). Plan must mark §1.9 ws-dep superseded.
   - `github.com/golang-jwt/jwt/v5` — INTENTIONALLY DEFERRED: stdlib
     HMAC-SHA256 stub (`internal/server/auth.go`, jwt-shaped, swap-ready;
     `TODO(jwt-v5)`). Plan must mark deferred to Phase 1.9-auth follow-up.
   - `github.com/fsnotify/fsnotify` — INTENTIONALLY SUBSTITUTED with stdlib
     polling (`internal/daemon/watcher.go` header +
     `docs/decisions/2026-09-17-interceptor-watcher.md`: zero new deps,
     SQLite-WAL hostile to fsnotify, Windows-friendly).
   - "Zero deps" claims (`go.mod … zero deps`, auth.go `TODO` saying "go.mod
     must stay dependency-free") are STALE: `go.mod` correctly carries
     pgx + pgvector. Correct wording is "stdlib-only except pgx/pgvector".
4. **Phase 6+ coverage:** plan Phase 6 (hardening: security §6.1, retention
   §6.2, offline §6.3, rate-limit §6.4, observability §6.5) is partially
   implemented — retention encoded in `infra/terraform/s3.tf` (180d
   GLACIER_IR), security in `internal/security` + JWT stub + force_ssl —
   but rate-limiting/observability/offline handling have no code yet.
   Mark 6.1–6.2 partial, 6.3–6.5 open.
5. **Binary existence (#74 closed by this):** plan target tree
   (`cmd/daemon/main.go`, `cmd/server/main.go`, `cmd/mem/main.go`) ALL
   EXIST. `deploy/README.md` no longer claims they are missing (updated in
   the deploy pass). Plan "Current Codebase" section is stale (references
   commit `524342e` single-user CLI) — refresh to current tree.
6. **Five-tier vs four-level memory model:** tracked separately by issue
   #30 (migration 001 CHECK has 4 levels; plan locks 5 tiers). Not
   resolved here; plan refresh must not silently pick a side.

## Suggested plan-refresh checklist (docs owner)

- [ ] Extend migration table to 008 + record 006–008 owners/phases.
- [ ] Add `security/governance/handoff/steering/migrate` to target tree.
- [ ] Rewrite §1.9 as landed/substituted/deferred (see §3 above) + fix
      zero-deps wording to "stdlib-only except pgx/pgvector".
- [ ] Phase 6: mark 6.1–6.2 partial, 6.3–6.5 open with issue links.
- [ ] Update "Current Codebase" snapshot + target-tree binary checkboxes.
- [ ] Cross-link ADR-045 (deploy env contract) and this file.
- [ ] Consider the CI/doc check the issue asks for: a lightweight inventory
      job (e.g. `ls migrations/*.up.sql` count vs plan table) so drift is
      visible — `postgres` job already asserts `migrations/*.up.sql` presence.
