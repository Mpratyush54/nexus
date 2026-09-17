# ISSUE-104 — Checkout Must Mutate (Workspace-Scoped Active Branch)

- **Issue:** #104 — audit: fake branch checkout endpoint and missing branch state mutation
- **Status:** Done (implementation + tests + docs)
- **Scope constraint:** ONLY `internal/store/branches.go` (+ `branches_test.go`), `internal/server/routes_extra.go` (+ `routes_extra_test.go`), `docs/`. No other files touched.

## What was built

| File | Contents |
|---|---|
| `internal/store/branches.go` | `BranchStore` gains `SetWorkspaceBranch(ctx, workspaceID, branch string) error`: blank branch → `branch is required` input error (server maps to 400), unknown workspace → `ErrNotFound` (server maps to 404). MemStore sets `ws.Branch` under `s.mu`; Postgres `UPDATE workspaces SET branch = $2, last_seen = now() WHERE id = $1::uuid` with `RowsAffected` check. `var _ BranchStore` assertions for both backends still compile |
| `internal/server/routes_extra.go` | `handleBranchCheckout` keeps existing resolution (ID first, then name+`?project_id=`, 404 if missing) and adds mutation: with `?workspace_id=` the branch NAME persists via `SetWorkspaceBranch` (unknown workspace → 404); without it the endpoint returns the branch (flattened `branchCheckoutView`, pre-#104 shape preserved) plus an explicit `nothing was persisted` note |
| `internal/store/branches_test.go` | `TestSetWorkspaceBranchMemStore` (persist visible via `GetActiveWorkspace`, unknown→`ErrNotFound`, blank→error) |
| `internal/server/routes_extra_test.go` | `TestBranchCheckoutPersistsWorkspace` (persist + resolve-only note + no-op persistence check + unknown branch 404 + unknown workspace 404 + blank name 400 via `/branches/%20/checkout`) |
| `docs/decisions/ADR-104-workspace-checkout.md` | Contract rationale |

## Decisions (see ADR-104 for rationale)

1. Workspace-scoped (not session-scoped): the `workspaces` row already owns a `branch` column + heartbeat lifecycle — no new table, no session plumbing.
2. Persist the branch NAME (matches `Workspace.Branch` git-branch semantics), not the branch UUID.
3. Resolve-only without `?workspace_id=` (backward compatible: old CLI callers get the same branch shape plus a `note`).

## Verification (2026-09-17, go1.27.0 windows/amd64)

- `gofmt -w` on touched files → clean
- `go build ./...` → exit 0
- `go vet ./internal/store/ ./internal/server/` → no findings
- `go test -count=1 ./internal/store/ ./internal/server/ ./internal/branches/` → all PASS (incl. new tests above; pre-existing `TestBranchListForkCheckoutDiffMerge` still green — checkout shape preserved)

## Follow-ups (not this issue)

- Live-Postgres verification of `SetWorkspaceBranch`.
- Default-workspace resolution (e.g. `?workspace_id=self` via active-workspace lookup) so callers need not track workspace ids.
- Auth scoping: any authenticated user can currently point any workspace at any branch (matches Heartbeat's trust model; tighten if workspaces gain owners).
