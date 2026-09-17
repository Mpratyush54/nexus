# ADR-039 — `cmd/` Entrypoints (daemon, server, mem)

- **ADR ID:** ADR-039-cmd-entrypoints
- **Date:** 2026-09-17
- **Author:** issue-#39 agent
- **Issue:** #39 `cmd/` entrypoints (plan target tree)
- **Status:** Accepted

## Context

`implementation-plan.md` (target directory structure) requires three local
entrypoints — `cmd/daemon/main.go`, `cmd/server/main.go`, `cmd/mem/main.go`
— but the repo has no `cmd/` directory at all. The only bootstraps in the
repo are `deploy/daemon-bootstrap` and `deploy/server-bootstrap`, which are
**container entrypoints, not CLI**: env-fixed defaults (`/workspace`,
`:7687`, `/app/migrations`), Aurora scale-from-zero connect retry,
migration-on-boot, and a `/readyz` probe for the orchestrator. They serve
ECS, not local development, and belong to issue #20.

Meanwhile the root `main.go` (`mem status`/`mem projects` skeleton) is owned
by the #21 CLI issue, and `daemon.New`, `server.New`, and
`(mcp.Server).ServeStdio` already exist with stable signatures — so the
missing piece is pure wiring.

## Options Considered

1. **Move the `deploy/*-bootstrap` mains into `cmd/`.**
   Pros: zero new code. Cons: conflates container boot (migrations, DB
   retry, `/readyz`, fixed image paths) with local CLI ergonomics (flags,
   cwd-relative roots, dev defaults); steals #20's files.
2. **(Chosen) Add thin `cmd/` wiring, leave everything else untouched.**
   `cmd/mem` (dispatch: `status`/`projects`/`mcp`), `cmd/server`
   (env-config `server.New` + serve), `cmd/daemon` (flag `-root`/`-port`/
   `-server` + `daemon.New`/`Start`/register/heartbeat). No edits to root
   `main.go`, `deploy/*`, `internal/*`, or `go.mod`/`go.sum`.
3. **Full CLI implementation inside `cmd/mem` now.**
   Rejected: the complete CLI is #21's scope; #39 ships dispatch + wiring
   only.

## Decisions

1. **Wiring only, no logic.** Each `cmd/` main parses flags/env, calls the
   existing constructor (`daemon.New`, `server.New`, `mcp.New`), and
   serves. Sandbox, auth, validation, and git helpers stay owned by #3/#8/#7.
2. **Reuse by calling the same funcs, not by importing root `main.go`.**
   Root command funcs are unexported `package main` and unimportable, so
   `cmd/mem` duplicates the ~10-line `status`/`projects` bodies against the
   same `internal/project` funcs (`Leaves`/`Fingerprint`/`CachedLeaves`) —
   identical behavior, and root `main.go` is untouched. Full #21 CLI stays
   with its owner.
3. **`cmd/server` fails closed like the bootstrap.** The Postgres-backed
   `server.Store` adapter is a follow-up, so `cmd/server` ships a local
   `stubStore` that 500s on data routes; `/healthz` is live. Local-dev
   differences from the bootstrap: no DB dial, no migrations, `PORT` default
   `8080`, and an insecure dev JWT default with a stderr warning when
   `JWT_SECRET` is unset (the bootstrap hard-requires ≥32 chars).
4. **`cmd/mem mcp` is local/ephemeral.** In-memory memory + episode stores
   (writes land as PROPOSED, nothing persists), `DaemonWorkspaceProvider` /
   `DaemonFileProxy` rooted at the cwd, default `HashEmbed`. Production
   wiring (Postgres `QuerierBackend`, LLM `Config.Embed`) arrives with
   #8/#10 without changing this entrypoint's shape.
5. **`cmd/daemon` mirrors bootstrap lifecycle with flags.** `-root`
   (default `.`), `-port` (default `7687` on `127.0.0.1`), `-server`
   (default `$CENTRAL_SERVER_URL`, empty = local-only). Register failure is
   non-fatal; the heartbeat loop retries presence — same contract as the
   bootstrap.

## Consequences

- `go build ./...` covers three new packages; `go vet ./cmd/...` and
  `gofmt` must stay clean (branch `fix/audit-gofmt-49`).
- `go run ./cmd/mem status` works with no env; `cmd/server` and
  `cmd/daemon` boot locally with zero config.
- Follow-ups (not #39): Postgres `server.Store` adapter replaces both
  stubs; #21 owns the full `mem` CLI; #8/#10 own MCP production wiring.
