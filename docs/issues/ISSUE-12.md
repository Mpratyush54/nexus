# ISSUE-12 — Sessions (Schema + Store + Isolation)

- **Issue:** #12 — Sessions (`migrations/003_sessions.up.sql`, store, promotion signal)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `migrations/003_*`, `internal/store/sessions.go`
  (+ tests), `docs/`. Did NOT touch `001_*`, `db.go`, `projects.go`,
  `workspaces.go`, `memory.go`, or any other package.
- **Plan ref:** `implementation-plan.md` §§3.1 (session schema), 3.3
  (session vs project scoping), 2.7 (SESSION → PROJECT promotion trigger);
  read `migrations/001_initial.up.sql` first for the `session_id` bare
  column and deferred-FK conventions reused here.

## What was built

| File | Contents |
|---|---|
| `migrations/003_sessions.up.sql` | `sessions` (plan §3.1 exactly); `session_participants` (surrogate PK + validity fix, see below); two partial unique active-seat indexes; `idx_sessions_project`, partial `idx_sessions_active`, `idx_session_participants_session`; `ALTER TABLE memory_items ADD CONSTRAINT fk_memory_session` + `idx_memory_session` |
| `migrations/003_sessions.down.sql` | Reverse-order rollback: drop FK → `idx_memory_session` → both tables (their indexes die with them); column/extensions kept |
| `internal/store/sessions.go` | Roles + `NormalizeRole`; `Session`/`Participant` types; `SessionStore`: `Create` (seats creator OWNER), `GetByID`, `End` (releases seats), `ListActive`, `Join` (idempotent active / fresh row after leave), `Leave`, `ListParticipants`, `CountKeySessionsDB`; pure `IsSessionScoped`, `IsVisibleToSession`, `FilterVisibleToSession`, `CountKeySessions`, `ShouldProposePromotion` (`PromotionThreshold = 3`), `PromotionCandidates` |
| `internal/store/sessions_test.go` | 17 DB-free tests: roles, join validation, isolation, promotion counter/candidates, store SQL paths via scripted `DBTX` fake |
| `internal/store/sessions_integration_test.go` | 2 `TEST_POSTGRES_DSN`-gated tests: multi-user join lifecycle; isolation-until-promoted end-to-end |
| `docs/decisions/ADR-012-session-isolation-promotion.md` | Why-mandatory ADR (PK validity fix, seam composition, isolation testability) |
| `docs/issues/ISSUE-12.md` | This file |

## Decisions (see ADR-012 for rationale)

1. Plan §3.1's `PRIMARY KEY (session_id, COALESCE(user_id,
   gen_random_uuid()))` is invalid Postgres (volatile in PK) → surrogate
   `id` PK preserving join/leave history + partial unique indexes enforcing
   single active seat per user/agent per session.
2. `agent_id` stays a bare UUID (FK deferred to 004 `agents` table — same
   pattern as 001's `episode_events.event_id`); `user_id → users`;
   `session_id → sessions ON DELETE CASCADE`; `memory_items → sessions`
   with NO cascade (memories outlive sessions for the promotion counter).
3. Isolation as pure predicates (`IsVisibleToSession`: session rows visible
   only in their session; everything else inherited) + one DB counter, so
   acceptance is unit-testable without Postgres.
4. FK scope kept to `memory_items` per spec; `episodes`/`tasks` session FKs
   left as follow-ups.

## Verification

- Structural SQL review of up/down (balanced parens/quotes, FK targets
  resolve against 001+003, down covers every object up creates): **passed**
  (output below).
- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/ -run TestSession` → unit tests **PASS**,
  live-DB tests **SKIP** (`TEST_POSTGRES_DSN` unset in this env; full
  output below).

Acceptance mapping: multi-user join (`TestSessionListParticipantsSQL` +
`TestSessionIntegration_MultiUserJoin`: creator OWNER, Bob joins → 2 seats,
re-join idempotent, leave → 1 seat, end → inactive); isolation until
promoted (`TestSessionVisibilityIsolation` +
`TestSessionIntegration_IsolationUntilPromoted`: sibling-invisible, counter
hits 3, post-promotion project-visible); promotion counter
(`TestSessionCountKeySessions`, `TestSessionPromotionCandidates`,
`TestSessionCountKeySessionsDB`).

## Follow-ups (not this issue)

- 004: `FOREIGN KEY (session_participants.agent_id) REFERENCES agents(id)`.
- `episodes.session_id` / `tasks.session_id` FKs (same one-line pattern).
- #10 processor: call `CountKeySessionsDB` per candidate key, auto-propose
  promotion at `>= 3`; decay/archival timers unchanged.
- Live apply of 003 up/down against Aurora/pgvector in CI (no live
  Postgres in this env).
