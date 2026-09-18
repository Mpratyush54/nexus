# ISSUE-102 — Memory Scope Isolation Leak + MCP Scope Enforcement + Session/Project Invariant

- **Issue:** #102 — audit: Memory scope isolation leak and invalid MCP scope combinations
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/store/memory.go`, `internal/store/sessions.go`, `internal/store/store.go` (MemStore.SearchMemory only), `internal/mcp/tools.go` (+ their `_test` files), `docs/`. No other files touched; no git operations. `ListSessionVisibleMemories` verified and left untouched.

## Findings (all three verified on master)

1. **NULL-project leak:** `PostgresStore.SearchMemory` + `SearchMemoryVector` used `WHERE (project_id = $1 OR project_id IS NULL)`, and `MemStore.SearchMemory` skipped the project check for every NULL-project row — so personal/session rows with NULL `project_id` were globally readable by every project.
2. **MCP invalid scope combos:** `handleMemoryWrite` accepted `level=personal`/`session` with no `user_id`/`session_id`, persisting `level='personal'` + `user_id=NULL` rows the DB cannot attribute.
3. **Cross-project session adoption:** `CreateSessionMemory` (both stores) checked session existence (MemStore) or nothing at all (Postgres) but never verified `session.project_id == memory.project_id`.

## What changed

| File | Change |
|---|---|
| `internal/store/memory.go` | Both search SQLs now `WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))`; header comment updated |
| `internal/store/store.go` | `MemStore.SearchMemory`: NULL-project rows visible only when `Level == "organization"` |
| `internal/store/sessions.go` | Both `CreateSessionMemory`: fetch session (`GetSession` / bucket lookup; unknown stays `ErrNotFound`), reject project mismatch with descriptive error naming both projects |
| `internal/mcp/tools.go` | `MemoryItem` + `memory_write` args gain optional `user_id`/`session_id`; `personal` requires non-blank `user_id`, `session` requires non-blank `session_id` (400 `invalidParams` otherwise); IDs set on stored item; tool description + schema document the args and that existence is enforced by the session flow (Store interface NOT widened) |
| tests | `TestSearchMemoryNullProjectOrgOnly` (memstore), `TestCreateSessionMemoryCrossProjectRejected` (sessions), `TestMemoryWriteScopeIdentity` (mcp) |
| `docs/decisions/ADR-102-memory-scope-isolation.md` | Rationale (org tier as global tier, no Store widening, blank-project session memories) |

## Verification

- `gofmt -w` on touched files; `go build ./...`; `go vet` + `go test -count=1` on `internal/store` + `internal/mcp` (no live DB).
- `ListSessionVisibleMemories` (both stores) verified already constrained: own session rows + `session_id IS NULL` + `level IN ('project','organization')` (Postgres) / `NewSessionInherits` + project match (MemStore) — left as-is per spec.

## Follow-ups (not this issue)

- Data migration/backfill for pre-existing NULL-project personal/session rows (now hidden from search but still in the table).
- `ListSessionVisibleMemories` Postgres path still inherits NULL-project `project`-level rows; harmonize with the org-only rule if desired (explicitly out of scope here).
- `memory_reflect` writes session-level rows without a `session_id`; same identity question applies if reflect rows ever need attribution.
