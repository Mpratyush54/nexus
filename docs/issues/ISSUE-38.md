# ISSUE-38 — Memory Lifecycle Endpoints (confirm / reject / promote)

- **Issue:** #38 — web/ Confirm/Reject/Promote buttons guaranteed 404
- **Status:** Done (implementation + docs; awaiting merge)
- **Assignee:** issue-38 agent
- **Scope constraint:** ONLY new `internal/server/lifecycle.go`, new
  `internal/server/lifecycle_test.go`, three route lines in
  `internal/server/server.go`, plus `docs/`. Did NOT touch
  `internal/server/routes.go` or any other package.
- **Plan refs:** `migrations/001_initial.up.sql` CHECK vocabularies;
  ADR-012 promotion rule (session→project clears `session_id`);
  `web/app.js` `memoryAction` (~lines 378-394) call shapes;
  `internal/store/memory.go` status vocab (`StatusProposed/...`).

## Problem

`web/app.js` calls `POST /memory/{id}/confirm | /reject | /promote`, but the
issue #8 route table had no such routes → every lifecycle click 404'd.

## What was built

| File | Contents |
|---|---|
| `internal/server/lifecycle.go` (new) | `ErrConflict` (→ 409), optional `lifecycleStore` (`GetMemory`/`UpdateMemory`, type-asserted so frozen `Store` is untouched), `handleConfirmMemory` (PROPOSED→CONFIRMED), `handleRejectMemory` (PROPOSED→REJECTED), `handlePromoteMemory` (session→project, status orthogonal; bodies ignored for web/ compat); unknown id → 404, wrong state → 409 `{"error":...}` |
| `internal/server/server.go` (+3 lines) | `POST /memory/{id}/confirm`, `/reject`, `/promote` registrations in `New` only — no other changes |
| `internal/server/lifecycle_test.go` (new) | `lifecycleFake` (embeds `fakeStore` + extension pair); 7 tests: confirm ok + re-confirm 409, reject ok + cross-transition 409s, confirmed→reject/confirm 409s, promote session→project ok (frontend `{level}` body tolerated) + non-session 409, unknown-id 404 ×3, auth-required 401, error-envelope shape |
| `docs/decisions/ADR-038-memory-lifecycle.md` | Why-mandatory ADR (options, CHECK-mirroring, body-ignoring rationale, adapter SQL contract) |
| `docs/issues/ISSUE-38.md` | This file |

## Decisions (see ADR-038 for rationale)

1. Extension interface + type assertion instead of widening `Store`
   (`server.go` freeze).
2. Promote ignores the `{level}` body: web/ sends `nextLevel("session") =
   "personal"` but the rule is fixed session→project; enforcing it would
   400 the real client. SQL adapter NULLs `session_id` (server type has no
   such field).
3. Status gates use `store.Status*` / `store.Level*` constants so API 409s
   mirror the DB CHECKs by construction.

## Verification

- `go build ./internal/...` → exit 0
- `go vet ./internal/server/` → exit 0
- `go test ./internal/server/` → ok (full package green, 1.7s), incl:
  - `TestMemoryConfirmLifecycle`, `TestMemoryRejectLifecycle`,
    `TestMemoryRejectAfterConfirm409`, `TestMemoryPromoteLifecycle`,
    `TestMemoryLifecycleUnknownID404`, `TestMemoryLifecycleRequiresAuth`,
    `TestMemoryLifecycleErrorEnvelope` — all PASS
- `gofmt -l` on owned files → clean
- Ownership: `git status` shows only `internal/server/lifecycle.go`,
  `internal/server/lifecycle_test.go`, `internal/server/server.go` (+3),
  `docs/decisions/ADR-038-memory-lifecycle.md`, `docs/issues/ISSUE-38.md`
- **Pre-existing breakage (not mine, not touched):** repo-wide
  `go build ./...` was already red from other agents' uncommitted work —
  `internal/server/wsbridge.go:264` (`*pgxpool.Pool` vs `store.DBTX`
  mismatch, untracked file) and `deploy/server-bootstrap/main.go` (unused
  `pgxpool` import, modified tracked file). Package-scoped verification
  above was run with the untracked `wsbridge*.go` parked aside and restored
  byte-identical afterwards (hash-verified).

## Follow-ups (not this issue)

- Postgres adapter: implement `GetMemory`/`UpdateMemory` per the reference
  SQL in `lifecycle.go` (promote MUST `SET session_id = NULL`).
- web/: drop the "lifecycle endpoints are a server follow-up" notice suffix
  in `memoryAction` once deployed (web/ out of scope here).
- `SUPERSEDED` transitions: no UI caller yet; reuse this pattern when needed.
