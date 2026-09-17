# ADR-034-missing-stores

- **ADR ID:** ADR-034-missing-stores
- **Date:** 2026-09-17
- **Author:** issue-34 subagent
- **Issue:** #34 users, tasks, watched_files tables have zero store coverage; watcher keeps last_hash in-memory only
- **Status:** Accepted

## Context

Migration 001 created seven tables, but only four had store coverage
(`projects`, `workspaces`, plus the memory/episode/session engines built on
top). Three tables — `users`, `tasks`, `watched_files` — had no Go code
touching them: user IDs referenced by every other store could not be
created or read, tasks had no lifecycle API, and the daemon watcher kept
`last_hash` in a memory-only map. A daemon restart therefore forgot every
instruction-file hash, so edits made while down were silently adopted as
the new baseline instead of surfacing as `INSTRUCTION_FILE_CHANGED`
events. Constraints: only `internal/store/users.go`, `tasks.go`,
`watched_files.go` (+ tests) may be added; `watcher.go` (+ test) is the
only daemon file touched; the daemon must NOT import the store package
(no Postgres dependency in the daemon); only two new docs.

## Options Considered

1. **Thin CRUD stores over the DBTX seam + local watcher persistence seam
   (chosen).** `UserStore` / `TaskStore` / `WatchedFileStore` follow the
   `projects.go`/`workspaces.go` conventions verbatim (NULL-coalescing
   column lists, `ErrNotFound` wrapping, validate-before-query); the
   watcher defines its own `WatchedFileStore` interface (local, no store
   import) with a JSON file-backed default and an in-memory test double.
2. **Have the daemon import `internal/store` and use the SQL
   `WatchedFileStore` directly.** Rejected: it drags pgx/pgvector into the
   daemon binary, violates the issue constraint, and couples the edge
   daemon's liveness to database reachability for what is fundamentally a
   local baseline file.
3. **Push hashes to the server on every change (no local persistence).**
   Rejected: doubles failure modes (offline daemon loses hashes), adds a
   network round-trip to the hot poll path, and still needs a local
   baseline for the first comparison after restart.
4. **Status transitions enforced only by the CHECK constraint.**
   Rejected: a DB error surfaces as a generic constraint violation with no
   machine-readable "illegal transition" signal, and it costs a round-trip
   to learn what pure code can decide. The Go machine mirrors the CHECK
   values; the constraint remains as defense in depth.

## Decision

- `internal/store/users.go` — `User` (`settings::TEXT` scan, mirroring the
  `events.go` `payload::TEXT` convention), `Create` (username required,
  settings default `'{}'`), `GetByID` / `GetByUsername` (the daemon login
  path), `UpdateSettings`, `Delete`, `List` (clamped limit).
- `internal/store/tasks.go` — `Task`, pure status machine
  (`IsValidTaskStatus` / `NormalizeTaskStatus` / `CanTransitionTaskStatus`;
  DONE is terminal except explicit reopen to OPEN; BLOCKED can never jump
  straight to DONE), `Create` (project/title/creator required, default
  OPEN), `GetByID`, `ListByProject` (optional status filter), `SetStatus`
  (read-then-validate-then-write, so illegal jumps fail before any write),
  `SetEpisode` (link/unlink the bug-fix arc), `Delete`.
- `internal/store/watched_files.go` — `WatchedFile`, `Watched*` file-type
  constants transcribing the migration 001 CHECK (mirroring the daemon's
  `InstructionFiles` table), `Upsert` on the natural key
  `UNIQUE(workspace_id, path)` (refreshes `last_hash` + `file_type` on
  conflict), `GetByWorkspacePath`, `ListByWorkspace` (path-ordered),
  `Delete`, and pure `StaleWatchedFiles` (stored rows vs. a fresh
  path→hash scan; untracked paths synthesize empty-hash entries).
- `internal/daemon/watcher.go` (only logic change) — local
  `WatchedFileStore` interface (`GetHash` / `SetHash` / `DeleteHash`, all
  keyed by workspace ID + rel path), `MemoryHashStore` (tests), and
  `FileHashStore` (JSON `{workspace: {path: hash}}`, rewritten per
  mutation, `0600`, corrupt file is an error rather than silent amnesia).
  `NewWatcherWithStore` installs persistence *before* the baseline
  snapshot so persisted hashes seed it; `checkPath` persists on change and
  `emitRemoval` drops on delete, both best-effort (persistence failure can
  never block detection; `PollOnce` remains the fallback).

## Why (Rationale)

- **Convention reuse is the risk control:** the three stores copy the
  exact patterns reviewers already accepted for projects/workspaces
  (DBTX seam, scripted fakes, validate-before-query), so the 30 new tests
  needed no new harness — and full-package runs prove no cross-issue
  breakage.
- **Persistence-before-snapshot is the correctness fix:** snapshotting
  disk hashes first and consulting the store second would re-adopt
  while-down edits as baseline. Seeding the baseline *from* the store
  makes the first post-restart `PollOnce` emit exactly the while-down
  edits (`TestWatchHashStoreSurfacesEditWhileDown` pins this).
- **Best-effort persistence keeps the daemon local-first:** a corrupt or
  unwritable state file degrades to the pre-#34 memory-only behavior
  instead of crashing the watcher; the server-side `watched_files` store
  remains the authoritative copy synced by callers.
- **Evidence:** `go build ./internal/...` OK; `go vet
  ./internal/store/ ./internal/daemon/` clean; `go test -count=1
  ./internal/store/ ./internal/daemon/` full packages PASS (9 user + 11
  task + 8 watched-file + 4 watcher-persistence tests new) — see
  `docs/issues/ISSUE-34.md`.

## Consequences

- All seven migration-001 tables now have store coverage; the daemon
  survives restarts without losing instruction-file baselines.
- Wiring follow-ups (not this issue): daemon startup constructing
  `NewWatcherWithStore` with a state-file path + workspace ID, and server
  sync of daemon hashes into `watched_files` via `Upsert`.
- Known limitation: `FileHashStore` rewrites the whole file per mutation —
  fine at four watched files per workspace, revisit if the watch list
  grows by orders of magnitude.

## Alternatives Rejected

See Options 2–4 above: daemon→store import (breaks layering + constraint),
server-push-only hashes (offline fragility, hot-path latency), CHECK-only
status enforcement (opaque errors, wasted round-trips).
