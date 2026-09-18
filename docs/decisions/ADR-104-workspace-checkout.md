# ADR-104 — Workspace-Scoped Branch Checkout

- **ADR ID:** ADR-104-workspace-checkout
- **Date:** 2026-09-17
- **Author:** subagent (issue #104)
- **Issue:** #104 Fake branch checkout endpoint, missing state mutation
- **Status:** Accepted

## Context

`POST /branches/{name}/checkout` resolved the branch and returned 200 with
zero state mutation — clients believed checkout succeeded while reads/writes
stayed on the old branch. The issue demands an exact contract (session- vs
workspace-scoped), persistence, and 400/404 behaviour. Ownership is limited to
`internal/store/branches.go`, `internal/server/routes_extra.go`, their tests,
and docs.

## Options Considered

1. **Session-scoped active branch.** Rejected: no session→branch pointer
   exists anywhere (schema, store, or server); inventing one means a new
   table/column plus session plumbing, oversized for this issue.
2. **(Chosen) Workspace-scoped active branch on the `workspaces` row.** The
   column (`branch`), lifecycle (register/heartbeat/active), and both-backend
   patterns already exist; checkout becomes one `UPDATE`-shaped method per
   backend, added to `BranchStore` (both backends implement it, assertions
   compile). Persist the branch NAME, matching `Workspace.Branch` git-branch
   semantics and heartbeat writes.
3. **Always-persist (require `workspace_id`).** Rejected: breaks the existing
   CLI shape (`POST {} to /branches/{name}/checkout`, no workspace id).
   Without `?workspace_id=` the endpoint stays resolve-only and says so in an
   explicit `note` — no more illusion of mutation.

## Decision

- `SetWorkspaceBranch(ctx, workspaceID, branch)`: blank → `branch is
  required` (400 via `isInputError` parity); unknown id → `ErrNotFound`
  (404). MemStore mutates under `s.mu`; Postgres updates `branch` (+
  `last_seen = now()`, heartbeat parity) with a `RowsAffected` check.
- Handler keeps ID-then-name+`?project_id=` resolution and 404s; the response
  flattens branch fields (`branchCheckoutView`) so pre-#104 decoders keep
  working, with sibling `workspace_id`/`note` fields carrying the outcome.

## Consequences

- Callers must pass `?workspace_id=` to mutate; nothing is inferred.
- Any authenticated user can point any workspace at any branch (same trust
  model as Heartbeat; tighten if workspaces gain ownership).
- Follow-ups: live-Postgres verification; default-workspace resolution so
  callers need not track workspace ids.
