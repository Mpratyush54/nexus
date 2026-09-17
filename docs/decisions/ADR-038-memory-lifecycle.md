# ADR-038 — Memory Lifecycle Endpoints (confirm / reject / promote)

- **ADR ID:** ADR-038-memory-lifecycle
- **Date:** 2026-09-17
- **Author:** issue-#38 agent
- **Issue:** #38 web/ lifecycle buttons 404 (`POST /memory/{id}/confirm|reject|promote`)
- **Status:** Accepted

## Context

`web/app.js` `memoryAction` (lines ~378-394) calls `POST /memory/{id}/confirm`,
`/reject`, `/promote` for the Memory Explorer buttons. The issue #8 route
table (`internal/server/routes.go` `registerRoutes`) never defined them, so
every click deterministically returned 404 and the UI surfaced a
"lifecycle endpoints are a server follow-up" notice (ADR-014 honest
degradation). Issue #38 fixes the server side under tight ownership
constraints:

1. `routes.go` untouchable; `server.go` may gain only the three route
   registrations, nothing else (notably NOT the `Store` interface).
2. `server.Memory` carries no `session_id` field; promotion's
   "session_id NULL" half must therefore live in the future SQL adapter.
3. Validation must mirror the `memory_items` CHECK constraints
   (`migrations/001_initial.up.sql`: status `PROPOSED/CONFIRMED/REJECTED/
   SUPERSEDED`, level `organization/project/personal/session`).

## Options Considered

1. **Widen `Store` in `server.go` with `GetMemory/UpdateMemory`.**
   Pros: simplest seam. Cons: violates the "register routes ONLY" freeze on
   `server.go`. Rejected.
2. **(Chosen) Optional `lifecycleStore` extension interface in the new
   `internal/server/lifecycle.go`, resolved by type assertion.**
   `GetMemory` + `UpdateMemory`; handlers return 500 when the wired store
   predates the extension. Zero edits to `routes.go` or the `Store`
   interface; exactly three `HandleFunc` lines added in `New`.
3. **Bypass the store: mutate via existing `SearchMemory` + `CreateMemory`.**
   Rejected — search cannot address a row by ID and create would duplicate
   rather than transition; objectively wrong tool.

## Decision

- `internal/server/lifecycle.go` (new): `ErrConflict` (→ 409), the
  `lifecycleStore` interface, and three handlers:
  - `POST /memory/{id}/confirm`: requires `PROPOSED` → sets `CONFIRMED`.
  - `POST /memory/{id}/reject`: requires `PROPOSED` → sets `REJECTED`.
  - `POST /memory/{id}/promote`: requires level `session` → sets `project`;
    status is orthogonal and untouched. The Postgres adapter MUST additionally
    `SET session_id = NULL` in the same update (server type has no session
    column; see ADR-012 promotion rule).
  - Unknown id → 404 via `store.ErrNotFound`; wrong state → 409 with the
    `{"error": ...}` envelope; bodies ignored (web/ sends `{}` and
    `{level: nextLevel(...)}` — promote's target is fixed session→project,
    so the body is accepted-but-ignored).
- `internal/server/server.go`: only three `mux.HandleFunc` lines in `New`.
- `internal/server/lifecycle_test.go` (new): `lifecycleFake` embedding the
  existing `fakeStore` plus the extension pair; covers happy paths,
  invalid-state 409s (re-confirm, confirm↔reject cross transitions,
  promote of non-session), unknown-id 404s ×3, auth-required, envelope shape.
- Docs: this ADR + `docs/issues/ISSUE-38.md`.

## Why (Rationale)

- **Constraint satisfaction is structural, not promised:** `git diff --stat`
  shows `server.go` +3 lines and `routes.go` untouched; the extension
  interface keeps the frozen `Store` seam intact while giving the Postgres
  adapter a two-method contract with reference SQL in the file comment.
- **CHECK-mirroring:** status/level gates use `store.StatusProposed`,
  `store.StatusConfirmed`, `store.StatusRejected`, `store.LevelSession`,
  `store.LevelProject` — the same constants as the migration CHECKs — so API
  409s and DB constraints agree by construction (mirrors the routes.go
  `allowed*` fail-fast pattern).
- **Frontend compatibility:** promote ignores `{level}` because web/ computes
  `nextLevel("session") = "personal"` while the server rule is fixed
  session→project; enforcing the body would turn the real client's request
  into a 400.
- **Evidence:** `go build ./internal/...` 0, `go vet ./internal/server/` 0,
  `go test ./internal/server/` green incl. 7 new lifecycle tests (full output
  in ISSUE-38). Note: repo-wide `go build ./...` was already red before this
  change from other agents' uncommitted work (`wsbridge.go` DBTX mismatch,
  `deploy/server-bootstrap` unused import) — verified pre-existing, untouched.

## Consequences

- Postgres adapter (follow-up) implements `GetMemory`/`UpdateMemory` with the
  reference SQL in `lifecycle.go`; promote MUST NULL `session_id`.
- Dashboard notice in `app.js` can drop the "server follow-up" suffix once
  deployed (web/ untouched by this issue).
- `SUPERSEDED` transitions remain unwired (no UI caller); a future
  supersede endpoint reuses this exact pattern.

## Alternatives Rejected

- Widening `Store` (option 1): breaks the explicit `server.go` freeze.
- Search/create reuse (option 3): cannot address-or-transition a row by ID.
