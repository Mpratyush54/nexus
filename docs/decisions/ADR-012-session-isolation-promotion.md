# ADR-012 — Sessions, Session Isolation & Promotion Signal

- **ADR ID:** ADR-012-session-isolation-promotion
- **Date:** 2026-09-17
- **Author:** issue-#12 agent
- **Issue:** #12 Sessions (`migrations/003_*` + `internal/store/sessions.go`)
- **Status:** Accepted

## Context

Phase 3 (plan §3) needs live multiplayer sessions: Alice and Bob join one
session, see each other's presence, and share session-scoped memories that
stay invisible to sibling sessions until promoted to project scope (plan
§§3.1, 3.3), with the plan §2.7 trigger — same fact in 3+ sessions proposes
SESSION → PROJECT promotion.

Constraints colliding here:

1. **Plan §3.1 SQL is not valid Postgres.** `PRIMARY KEY (session_id,
   COALESCE(user_id, gen_random_uuid()))` puts a volatile function in a
   primary key (rejected: functions in index expressions must be immutable)
   and could never represent re-join history anyway.
2. **Parallel ownership** — `001_initial`, `db.go`, `projects.go`,
   `workspaces.go`, `memory.go` belong to other issues; this issue owns only
   `migrations/003_*`, `internal/store/sessions.go` (+ tests), `docs/`. The
   session store must compose with the existing `DBTX`/`MemoryItem` seam
   untouched.
3. **002 is missing.** `migrations/` contains only `001_*`; there is no
   `events` table yet, and the `agents` table lands in 004 — so `agent_id`
   cannot carry a foreign key today.
4. **Acceptance is behavioural** (multi-user join, isolation until promoted),
   so the isolation rule must be testable without a live database.

## Options Considered

1. **Transcribe plan §3.1 verbatim (volatile PK).**
   Pros: zero deviation. Cons: migration fails to apply on any Postgres —
   objectively broken, rejected on validity alone.
2. **(Chosen) Surrogate PK + partial unique active-seat indexes + pure
   isolation/promotion predicates.**
   `session_participants.id UUID PK`; `UNIQUE(session_id, user_id) WHERE
   active` + `UNIQUE(session_id, agent_id) WHERE active`; `agent_id` bare
   UUID with FK deferred to 004 (the exact deferred-FK pattern 001 used for
   `episode_events.event_id → events(id)`); `IsVisibleToSession`,
   `FilterVisibleToSession`, `CountKeySessions`, `ShouldProposePromotion`,
   `PromotionCandidates` as pure funcs; one DB-backed counter
   (`CountKeySessionsDB`) for the Memory Processor.
3. **Enforce isolation only in SQL views / RLS.**
   Pros: DB-guaranteed. Cons: Aurora Serverless v2 + pgx + RLS session-vars
   add operational surface no other issue uses; untestable without live
   Postgres; the read-path `WHERE session_id IS NULL OR session_id = $1`
   plus the pure predicate gives the same guarantee with DB-free tests.

## Decision

- `migrations/003_sessions.up.sql`: `sessions` per plan §3.1 exactly
  (`project_id → projects`, `created_by → users NOT NULL`, `is_active`,
  `created_at`/`ended_at`); `session_participants` with surrogate id,
  `session_id → sessions ON DELETE CASCADE`, `user_id → users`, bare
  `agent_id` (FK in 004), `role` CHECK, `joined_at`/`left_at`,
  `CHECK (user_id IS NOT NULL OR agent_id IS NOT NULL)`, two partial unique
  active-seat indexes, `idx_sessions_project`, partial `idx_sessions_active`,
  `idx_session_participants_session`; `ALTER TABLE memory_items ADD
  CONSTRAINT fk_memory_session … REFERENCES sessions(id)` (no cascade —
  memories outlive sessions for the promotion counter) + `idx_memory_session`.
- `migrations/003_sessions.down.sql`: drop FK, `idx_memory_session`, both
  tables (their indexes die with them); column and extensions untouched.
- `internal/store/sessions.go`: `SessionStore` (`Create` seats creator
  OWNER, `GetByID`, `End` releases seats, `ListActive`, `Join` idempotent on
  active seat / fresh row after `Leave`, `ListParticipants` active-only,
  `CountKeySessionsDB`); roles + `NormalizeRole`; `JoinParams.Validate`;
  pure `IsSessionScoped`, `IsVisibleToSession`, `FilterVisibleToSession`,
  `CountKeySessions`, `ShouldProposePromotion` (`PromotionThreshold = 3`),
  `PromotionCandidates` (deterministic sort).
- FK scope kept to `memory_items` only (issue spec); `episodes.session_id`
  / `tasks.session_id` FKs are a follow-up, not silent scope creep.

## Why (Rationale)

- **Validity fix is forced, not stylistic:** volatile functions are illegal
  in PKs, so option 1 cannot apply — verified by inspection (no live
  Postgres in this env; structural SQL review + `RunMigrations`-compatible
  plain DDL instead). History-preserving rows + partial unique indexes keep
  every property the plan wanted (one active seat per user) while allowing
  leave/re-join, which the plan's PK could not express.
- **Seam composes untouched:** `SessionStore` uses only `DBTX`,
  `nullText`/`nullUUID`, `ErrNotFound`, `MemoryItem` from sibling files —
  no edits to other owners' files (verified: `git status` shows only
  `migrations/003_*`, `internal/store/sessions*.go`, `docs/`).
- **Isolation is proven DB-free:** `TestSessionVisibilityIsolation` asserts
  sibling-invisibility + post-promotion visibility; `TestSessionCountKeySessions`
  asserts same-session duplicates collapse to one vote and project copies
  don't vote; `TestSessionIntegration_IsolationUntilPromoted` (gated on
  `TEST_POSTGRES_DSN`) replays the same rule against real rows.
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `go test ./internal/store/ -run TestSession` unit tests pass; live-DB
  tests skip cleanly without `TEST_POSTGRES_DSN`.

## Consequences

- 004 must add `FOREIGN KEY (agent_id) REFERENCES agents(id)` (or
  document why not) — `agent_id` is currently unenforced.
- `episodes.session_id` / `tasks.session_id` FKs still open (follow-up).
- Memory Processor (#10) calls `CountKeySessionsDB` per candidate key and
  promotes at `>= PromotionThreshold`; auto-confirm timers unchanged.
- RLS / SQL-view enforcement explicitly deferred (option 3).

## Alternatives Rejected

- Verbatim plan PK (option 1): invalid Postgres, fails to apply.
- RLS/view enforcement (option 3): operational cost + untestable without
  live DB, for no extra Phase-3 guarantee.
