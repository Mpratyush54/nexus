# ADR-046 — CI: -race + gofmt Gate + Live-Postgres Suite

- **ADR ID:** ADR-046-ci-race-postgres
- **Date:** 2026-09-17
- **Author:** fix/audit-gofmt-49 agent
- **Issue:** #46 CI must run `-race`, enforce gofmt, and exercise live-DB tests
- **Status:** Accepted

## Context

Three gaps, all documented across prior issues but never closed in one place:
`go test -race` is unrunnable in this environment (MinGW.org GCC 6.3.0,
32-bit — `cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`, see
`docs/issues/ISSUE-2.md` verification); the `TEST_POSTGRES_DSN`-gated tests
in `internal/store` (`integration_test.go`, `sessions_integration_test.go`,
`events_test.go`) always SKIP here, so migrations 001–005 are never applied
against a live schema locally; and there is no gofmt gate, so formatting
regressions (cf. #49) land silently. `go.mod` pins `go 1.26.1`, so CI must
use Go 1.26+.

## Options Considered

1. **GitHub Actions, 3 jobs (build matrix + test-race + postgres service)** —
   pros: one file, no new deps, matrix proves Windows builds, service
   container gives the exact prod extension set (pgvector on PG16), gated
   tests run unmodified via `TEST_POSTGRES_DSN`. Cons: Actions minutes;
   Windows job pays a mingw install on every run.
2. **Single-job workflow (build+test+postgres all in one)** — pros: fewer
   minutes, simpler deps. Cons: no Windows coverage, no isolation between
   the fast static gate and the slow live-DB suite; a DB flake hides vet/
   gofmt signal.
3. **Local-only script (`scripts/ci.sh`) with no hosted CI** — pros: zero
   minutes. Cons: reproduces the exact local limitation (32-bit gcc, no
   Postgres) that motivated the issue; unenforceable on PRs.

## Decision

- New file only: `.github/workflows/ci.yml` (Go `1.26.x` via
  `actions/setup-go@v5`):
  - `build` (matrix `ubuntu-latest` / `windows-latest`): `go build ./...`,
    `go vet ./...`, `gofmt -l` fail-on-output (run under `shell: bash` so
    the check is identical on Windows); Windows installs `mingw` (mingw-w64,
    64-bit gcc) via Chocolatey so `-race` cgo works there.
  - `test-race` (`ubuntu-latest`, `needs: build`): `go test -race -count=1
    ./...` (gated DB tests SKIP here by design).
  - `postgres` (`ubuntu-latest`, `needs: build`, service
    `pgvector/pgvector:pg16`, ports `5432:5432`, `POSTGRES_USER/PASSWORD/DB`
    = `postgres/postgres/nexus_test`, `pg_isready` health check):
    `TEST_POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/nexus_test?sslmode=disable`
    + `go test -count=1 ./...` — the gated tests apply `migrations/*.up.sql`
    via `RunMigrations`, so migrations-apply is covered with no extra
    harness.
- `docs/issues/ISSUE-46.md` records scope, verification, and follow-ups.
- No other files touched: no `go.mod`/`go.sum` changes, no test edits, no
  migration edits.

## Why (Rationale)

- **3 jobs beat 1 (Option 1 beats 2):** the `build` matrix is the fast,
  always-required gate (catches #49-class gofmt regressions on both OSes in
  seconds); `test-race` and `postgres` are slower and flakier by nature, so
  isolating them behind `needs: build` keeps their signal clean and lets
  them run in parallel once the static gate passes.
- **`pgvector/pgvector:pg16` is the only honest service image:** the schema
  (`001_initial.up.sql` + `002_events` + `005_branches`) uses vector
  columns/indexes; stock `postgres:16` would fail migration-apply, and a
  version skew (pg15/pg17) would test a schema the deploy never runs.
- **No test-code changes needed:** the `TEST_POSTGRES_DSN` gate with
  `t.Skip` when unset (see `internal/store/integration_test.go:25-27`) is
  exactly the seam CI needs — set the env var and the same suite that SKIPs
  locally goes live, applying real migrations through the production
  `RunMigrations` path rather than a throwaway SQL script.
- **Go `1.26.x` (not pinned patch):** `go.mod` declares `go 1.26.1`;
  floating the patch picks up security fixes while the `1.26` floor keeps
  the language/toolchain semantics the repo was written against.
- **Evidence:** Actions cannot run in this environment (no Go toolchain,
  32-bit-only gcc, no Docker/Postgres) — verification here is a YAML
  self-review (service image, ports, env, job deps; see ISSUE-46
  checklist). First green run on push/PR is the closing proof and is
  integrator-owned.

## Consequences

- Every push/PR pays: build matrix (~2 OSes) + race suite + live-DB suite.
  If minutes become a concern, restrict the Windows leg to the `build` job
  (already the case — race/DB run Linux-only).
- Contributors must keep `gofmt` clean; the `build` job fails otherwise.
- Follow-ups: first green Actions run; consider `-race` on the `postgres`
  job once timing is known; consider caching (`actions/cache` for GOMODCACHE)
  if the suite grows slow.

## Alternatives Rejected

- **Single-job workflow (Option 2):** rejected — conflates static-gate
  signal with DB flakes and drops Windows coverage entirely.
- **Local-only script (Option 3):** rejected — cannot fix the environment
  limitation that is the premise of the issue, and is unenforceable on PRs.
