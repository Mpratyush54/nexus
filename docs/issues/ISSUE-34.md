# ISSUE-34 — [Phase 1] Missing stores: users, tasks, watched_files + watcher hash persistence

- **Status:** Done
- **Scope:** `internal/store/` (`users.go`, `tasks.go`, `watched_files.go`,
  `users_test.go`, `tasks_test.go`, `watched_files_test.go`) +
  `internal/daemon/watcher.go` (`WatchedFileStore` seam,
  `MemoryHashStore`, `FileHashStore`, `NewWatcherWithStore`,
  persistence wiring) + `internal/daemon/watcher_test.go` (4 new tests)
  and `docs/` only. No other daemon files, no migrations, no `go.mod` /
  `go.sum` changes.
- **Plan ref:** `implementation-plan.md` §1.1 (schema: users, tasks,
  watched_files DDL), §§1.3/2.2 (watcher `last_hash`). Full rationale:
  `docs/decisions/ADR-034-missing-stores.md`.

## What was built

- `internal/store/users.go` — `UserStore` over the `DBTX` seam:
  `Create` (username required; email optional; settings default `'{}'`),
  `GetByID`, `GetByUsername` (daemon/server login path: registration
  carries a username, not a UUID), `UpdateSettings` (whole-document
  replace), `Delete`, `List` (newest-first, clamped limit). Settings
  selected as `settings::TEXT`, mirroring the `events.go` `payload::TEXT`
  convention so the JSONB column scans into a plain string.
- `internal/store/tasks.go` — `TaskStore` plus a pure status machine:
  `IsValidTaskStatus` / `NormalizeTaskStatus` (`""` → `OPEN`) /
  `CanTransitionTaskStatus` (DONE terminal except explicit reopen to
  OPEN; BLOCKED routes back through OPEN/IN_PROGRESS, never straight to
  DONE; no-op rewrites allowed). `Create` (project/title/creator
  required), `GetByID`, `ListByProject` (optional validated status
  filter), `SetStatus` (read → validate → write; illegal jumps fail
  before any write), `SetEpisode` (link/unlink the bug-fix arc),
  `Delete`.
- `internal/store/watched_files.go` — server-side hash persistence:
  `Watched*` type constants transcribing the migration 001 CHECK,
  `Upsert` on `UNIQUE(workspace_id, path)` (conflict refreshes
  `last_hash` + `file_type`), `GetByWorkspacePath`, `ListByWorkspace`
  (path-ordered for deterministic scan comparison), `Delete`, and pure
  `StaleWatchedFiles` (stored rows vs. fresh path→hash scan; changed +
  never-scanned + untracked paths reported, sorted by path).
- `internal/daemon/watcher.go` — local `WatchedFileStore` interface
  (`GetHash`/`SetHash`/`DeleteHash` by workspace ID + rel path; defined
  here, `internal/store` deliberately NOT imported), `MemoryHashStore`
  (mutex-guarded, for tests), `FileHashStore` (JSON
  `{workspace: {path: hash}}`, `0600`, parents created, corrupt file is a
  load error not silent amnesia). `NewWatcherWithStore` installs
  persistence before the baseline snapshot so persisted hashes seed it;
  `SetWorkspaceID`/`SetHashStore` setters for late binding; `checkPath`
  persists on change, `emitRemoval` drops on delete — both best-effort so
  persistence failure degrades to pre-#34 memory-only behavior instead of
  blocking detection.
- Tests — 32 DB-free unit tests via scripted DBTX fakes (sessions_test.go
  pattern: row queue + canned result sets + recorded statements/args):
  9 user (validation blocks DB, settings default, username lookup +
  trim, not-found wraps `ErrNotFound`, list SQL), 11 task (machine table
  incl. DONE-terminal/BLOCKED→DONE-forbidden, create defaults OPEN,
  read-then-write `SetStatus`, illegal transition writes nothing,
  link/unlink, status filter SQL), 8 watched-file (type validation,
  natural-key upsert SQL, workspace+path scoping, path ordering, pure
  staleness incl. synthesized untracked entries); plus 4 daemon tests
  (`TestWatchHashStorePersistsAcrossRestarts`,
  `TestWatchHashStoreSurfacesEditWhileDown`,
  `TestWatchHashStoreDeletionClearsPersistedHash`,
  `TestFileHashStoreRoundTrip` incl. corrupt-file rejection).
- `docs/decisions/ADR-034-missing-stores.md` — full ADR with mandatory
  Why section. This file (`docs/issues/ISSUE-34.md`).

## Verification

- `go build ./internal/...`: **clean** (all internal packages incl.
  parallel agents' work).
- `go build ./...`: **fails outside scope** in
  `deploy/server-bootstrap/main.go` (`undefined: server.BridgeEvents`) —
  another agent's uncommitted in-flight work (`internal/server/*` also
  modified by others); untouched by and unrelated to this issue.
- `go vet ./internal/store/ ./internal/daemon/`: **clean, no findings**.
- `gofmt -l` on the six new store files: **clean** (`watcher.go` /
  `watcher_test.go` edits preserve the files' existing CRLF endings;
  whole-file gofmt flag on those predates this change — HEAD versions
  flag identically).
- `go test -count=1 ./internal/store/ ./internal/daemon/`: **both
  packages PASS** (full runs: store 0.27s, daemon 5.66s incl. the
  live-fsnotify test), no skips, no new failures in pre-existing tests.

## Follow-ups

- Daemon startup: construct via `NewWatcherWithStore` with a state-file
  path + workspace ID (currently opt-in; default remains memory-only).
- Server sync: reconcile daemon hashes into `watched_files` via `Upsert`
  and use `StaleWatchedFiles` for the Memory Processor's scan
  comparison.
- Live-Postgres coverage for the three stores (TEST_POSTGRES_DSN-gated
  integration tests, mirroring issue #2's) when a pgvector instance is
  available.
