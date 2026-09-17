# ISSUE-39 — [Phase 1] `cmd/` Entrypoints (daemon, server, mem)

- **Issue:** #39 — `cmd/` entrypoints (plan target tree: `cmd/daemon`,
  `cmd/server`, `cmd/mem`)
- **Status:** Done (implementation + docs; awaiting merge)
- **Assignee:** issue-#39 agent
- **Scope constraint:** ONLY new dir `cmd/` + `docs/`. Did NOT touch root
  `main.go`, `deploy/*-bootstrap`, `internal/*`, `go.mod`/`go.sum`, or any
  other dir.

## Problem

The plan requires `cmd/daemon`, `cmd/server`, and `cmd/mem`; the repo had
none. The `deploy/*-bootstrap` mains are container entrypoints (ECS-fixed
env, Aurora retry, migration-on-boot, `/readyz`), not local CLI.

## What was built

| File | Contents |
|---|---|
| `cmd/mem/main.go` | Dispatch: `status`, `projects` (same `internal/project` funcs as root CLI: `Leaves`/`Fingerprint`/`CachedLeaves`), `mcp` → `(mcp.Server).ServeStdio` with local wiring (in-memory stores, `DaemonWorkspaceProvider`/`DaemonFileProxy` at cwd, default `HashEmbed`); signal-aware ctx; `context.Canceled` treated as clean shutdown |
| `cmd/server/main.go` | Env-config (`PORT` default `8080`, `JWT_SECRET` with insecure dev default + warning) start calling `server.New`; fail-closed `stubStore` (500s on data routes, `/healthz` live) until the Postgres adapter follow-up lands |
| `cmd/daemon/main.go` | Flags `-root` (default `.`), `-port` (default `7687` on `127.0.0.1`), `-server` (default `$CENTRAL_SERVER_URL`, empty = local-only); `daemon.New` → `Start` → `Register` (non-fatal) + 30s heartbeat loop; signal-aware shutdown |
| `docs/decisions/ADR-039-cmd-entrypoints.md` | Why-mandatory ADR (bootstrap vs CLI split, thin-wiring, fail-closed stub, local/ephemeral MCP, #21 ownership) |
| `go.mod` / `go.sum` | Untouched — zero new dependencies |

## Decisions (see ADR-039 for rationale)

1. Thin wiring only — no logic moves into `cmd/`; full #21 CLI stays with
   its owner and root `main.go` is unedited (its funcs are unexportable, so
   `cmd/mem` calls the same `internal/project` funcs instead).
2. `deploy/*-bootstrap` files untouched — container boot and local CLI are
   separate concerns owned by #20.
3. `cmd/server` and `cmd/mem mcp` are explicitly ephemeral today
   (fail-closed stub; in-memory PROPOSED-only stores) — no silent
   persistence.

## Verification (2026-09-17, in-repo, branch `fix/audit-gofmt-49`)

- `go build ./...` → exit 0
- `go vet ./cmd/...` → exit 0
- `go run ./cmd/mem status` → prints status block, exit 0
- `gofmt -l cmd/` → clean
